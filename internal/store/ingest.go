package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// maxClockSkew is how far ahead of the server clock a report may claim to
// have been observed before it is rejected.
const maxClockSkew = 10 * time.Minute

// Ingest applies one inventory report. Reports must arrive in observed_at
// order per host; one older than the last applied report is rejected as
// stale (the agent's spool replays in order, so this only catches bugs).
// One observed in the same second as the last is fine: it is a heartbeat,
// or a change noticed twice in quick succession.
// A report whose normalised content matches the host's current snapshot is
// a heartbeat: it extends timestamps and writes nothing else. Otherwise a
// new snapshot is recorded and diffed against the host's open placements,
// producing closed intervals and events. Everything happens in one
// transaction.
func (s *Store) Ingest(ctx context.Context, r Report) (IngestResult, error) {
	now := s.now()
	if r.ObservedAt.After(now.Add(maxClockSkew)) {
		return IngestResult{RejectReason: "clock_skew"}, nil
	}
	if r.ObservedAt.IsZero() {
		r.ObservedAt = now
	}
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IngestResult{}, err
	}
	defer sqlTx.Rollback()
	t := &tx{Tx: sqlTx, ctx: ctx, now: now.Unix(), obs: r.ObservedAt.Unix()}

	host, err := t.upsertHost(r.Host)
	if err != nil {
		return IngestResult{}, err
	}
	if host.lastObserved.Valid && t.obs < host.lastObserved.Int64 {
		return IngestResult{RejectReason: "stale"}, nil
	}

	rows := make([]devRow, len(r.Devices))
	for i, d := range r.Devices {
		rows[i].dev = d
		rows[i].uses = canonicalUses(d.Uses)
		if d.Error != "" {
			continue
		}
		rows[i].driveID, rows[i].created, err = t.resolveDrive(d)
		if err != nil {
			return IngestResult{}, err
		}
	}
	hash := contentHash(rows, r.Unmapped, r.Complete)

	result := IngestResult{Accepted: true}
	if host.currentSnapshotID.Valid && hash == host.currentHash {
		err = t.heartbeat(host)
	} else {
		result.Changed = true
		err = t.applySnapshot(host, r, rows, hash, 0)
	}
	if err != nil {
		return IngestResult{}, err
	}
	if err := t.resumeIfStale(host); err != nil {
		return IngestResult{}, err
	}
	if err := t.ingestSAS(host, r, rows); err != nil {
		return IngestResult{}, err
	}
	if err := t.ingestDIMMs(host, r); err != nil {
		return IngestResult{}, err
	}
	if r.Complete {
		if err := t.noteEnclosures(host, r, rows); err != nil {
			return IngestResult{}, err
		}
	}
	if _, err := t.ExecContext(ctx, `UPDATE host SET last_report = ?, last_observed = ? WHERE host_id = ?`, t.now, t.obs, host.id); err != nil {
		return IngestResult{}, err
	}
	result.Statuses, err = t.statuses(rows)
	if err != nil {
		return IngestResult{}, err
	}
	return result, sqlTx.Commit()
}

// devRow is a reported device with the drive it resolved to.
type devRow struct {
	dev     ReportDevice
	driveID int64 // 0: unidentified
	created bool  // the drive record was created by this report
	uses    string
}

// canonicalUses is the JSON form of a use list, sorted, that placements and
// hashes compare.
func canonicalUses(uses []string) string {
	u := slices.Clone(uses)
	sort.Strings(u)
	if u == nil {
		u = []string{}
	}
	b, _ := json.Marshal(u)
	return string(b)
}

// contentHash summarises what matters about a report for change detection:
// which drives are where with which uses and states, which devices could not
// be identified, and which pool members are unmapped.
func contentHash(rows []devRow, unmapped []PoolMember, complete bool) string {
	lines := make([]string, 0, len(rows)+len(unmapped)+1)
	for _, r := range rows {
		lines = append(lines, fmt.Sprintf("d|%d|%s|%s|%s|%s|%s|%s|%s", r.driveID, r.dev.DevName, enclosureKey(r.dev), enclosureVia(r.dev), r.dev.Bay, r.uses, r.dev.MemberState, r.dev.Error))
	}
	for _, m := range unmapped {
		lines = append(lines, fmt.Sprintf("u|%s|%s|%s|%s", m.Pool, m.Path, m.GUID, m.State))
	}
	lines = append(lines, fmt.Sprintf("c|%v", complete))
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// heartbeat extends the current snapshot and every open interval on the
// host to the report's time.
func (t *tx) heartbeat(host *hostRow) error {
	for _, q := range []string{
		`UPDATE snapshot SET last_at = ? WHERE snapshot_id = ?`,
		`UPDATE placement SET last_seen = ? WHERE host_id = ? AND ended_at IS NULL`,
		`UPDATE ghost SET last_seen = ? WHERE host_id = ? AND ended_at IS NULL`,
	} {
		arg := any(host.id)
		if strings.HasPrefix(q, "UPDATE snapshot") {
			arg = host.currentSnapshotID.Int64
		}
		if _, err := t.ExecContext(t.ctx, q, t.obs, arg); err != nil {
			return err
		}
	}
	return nil
}

func (t *tx) resumeIfStale(host *hostRow) error {
	if !host.staleSince.Valid {
		return nil
	}
	if _, err := t.ExecContext(t.ctx, `UPDATE host SET stale_since = NULL WHERE host_id = ?`, host.id); err != nil {
		return err
	}
	return t.event(EventHostResumed, 0, host.id, map[string]any{"stale_since": host.staleSince.Int64, "silent_secs": t.obs - host.staleSince.Int64}, "report", 0)
}

// enclosureKey is what placements are keyed on: the SES enclosure the
// drive sits in, or failing that the SAS address of the node that reaches
// it (the expander, or the HBA for its own bays), or failing that the
// kernel's name for that node. Agents before 0.7 sent only the expander,
// so their drives key on its address as before. The kernel's name is kept
// beside the key as enclosure_via for display.
func enclosureKey(d ReportDevice) string {
	switch {
	case d.EnclosureID != "":
		return d.EnclosureID
	case d.EnclosureViaID != "":
		return d.EnclosureViaID
	case d.ExpanderID != "":
		return d.ExpanderID
	case d.EnclosureVia != "":
		return d.EnclosureVia
	}
	return d.Expander
}

func enclosureVia(d ReportDevice) string {
	if d.EnclosureVia != "" {
		return d.EnclosureVia
	}
	return d.Expander
}

type placementRow struct {
	id           int64
	driveID      int64
	hostID       int64
	hostname     string
	enclosure    string // the key
	enclosureVia string
	bay          string
	uses         string
	devName      string
	firstSeen    int64
	lastSeen     int64
	endReason    string
}

// applySnapshot records a changed state and diffs it against the host's open
// placements and ghosts. With replay set, the snapshot already exists
// (Rebuild is replaying it): no snapshot rows are written and ghosts are
// left alone, since reports' unmapped members are not kept in snapshots.
func (t *tx) applySnapshot(host *hostRow, r Report, rows []devRow, hash string, replay int64) error {
	open, err := t.openPlacements(host.id)
	if err != nil {
		return err
	}
	byDrive := map[int64]*placementRow{}
	byBay := map[string]*placementRow{}
	for _, p := range open {
		byDrive[p.driveID] = p
		if p.enclosure != "" {
			byBay[p.enclosure+"\x00"+p.bay] = p
		}
	}

	// Which drives the report accounts for: identified ones, plus the drive
	// an unidentified device is attributed to by sitting in its bay.
	present := map[int64]bool{}
	for _, row := range rows {
		if row.driveID != 0 {
			present[row.driveID] = true
		}
	}
	degraded := !r.Complete
	var unattributed []string
	for _, row := range rows {
		if row.dev.Error == "" || row.driveID != 0 {
			continue
		}
		if p := byBay[enclosureKey(row.dev)+"\x00"+row.dev.Bay]; p != nil && enclosureKey(row.dev) != "" && !present[p.driveID] {
			present[p.driveID] = true
			if _, err := t.ExecContext(t.ctx, `UPDATE placement SET last_seen = ? WHERE placement_id = ?`, t.obs, p.id); err != nil {
				return err
			}
			continue
		}
		degraded = true
		unattributed = append(unattributed, row.dev.DevName)
	}

	snapshotID := replay
	if replay == 0 {
		errs, _ := json.Marshal(append([]string{}, r.CollectorErrors...))
		res, err := t.ExecContext(t.ctx, `INSERT INTO snapshot (host_id, first_at, last_at, received_at, content_hash, complete, device_count, errors) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			host.id, t.obs, t.obs, t.now, hash, !degraded, len(rows), string(errs))
		if err != nil {
			return err
		}
		snapshotID, _ = res.LastInsertId()
		for _, row := range rows {
			links, _ := json.Marshal(append([]string{}, row.dev.DevLinks...))
			if _, err := t.ExecContext(t.ctx, `INSERT INTO snapshot_device (snapshot_id, dev_name, drive_id, expander, expander_id, bay, enclosure_path, size_bytes, uses, member_state, dev_links, scsi_addr, error, enclosure_id, enclosure_via, enclosure_via_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				snapshotID, row.dev.DevName, nullID(row.driveID), row.dev.Expander, row.dev.ExpanderID, row.dev.Bay, row.dev.EnclosurePath, row.dev.SizeBytes, row.uses, row.dev.MemberState, string(links), row.dev.SCSIAddr, row.dev.Error, row.dev.EnclosureID, row.dev.EnclosureVia, row.dev.EnclosureViaID); err != nil {
				return err
			}
		}
	}
	source := fmt.Sprintf("snapshot:%d", snapshotID)

	prevStates, err := t.memberStates(host.currentSnapshotID)
	if err != nil {
		return err
	}

	// A report with a failed collector stage has unreliable uses: record it,
	// keep every interval alive, and change nothing.
	if !r.Complete {
		if _, err := t.ExecContext(t.ctx, `UPDATE placement SET last_seen = ? WHERE host_id = ? AND ended_at IS NULL`, t.obs, host.id); err != nil {
			return err
		}
	} else {
		if err := t.renameEnclosures(host, rows, byDrive, source, snapshotID); err != nil {
			return err
		}
		for _, row := range rows {
			if row.driveID == 0 {
				continue
			}
			if err := t.placeDrive(host, row, byDrive[row.driveID], source, snapshotID); err != nil {
				return err
			}
			if prev, ok := prevStates[row.driveID]; ok && prev != row.dev.MemberState {
				if err := t.event(EventMemberStateChanged, row.driveID, host.id, map[string]any{"from": prev, "to": row.dev.MemberState, "uses": json.RawMessage(row.uses)}, source, snapshotID); err != nil {
					return err
				}
			}
		}
		for _, p := range open {
			if present[p.driveID] || degraded {
				continue
			}
			if err := t.vanish(p, r.Unmapped, source, snapshotID); err != nil {
				return err
			}
		}
	}

	if degraded && !host.degraded {
		if err := t.event(EventReportDegraded, 0, host.id, map[string]any{"unidentified": append([]string{}, unattributed...), "collector_errors": append([]string{}, r.CollectorErrors...)}, source, snapshotID); err != nil {
			return err
		}
	}
	if replay == 0 {
		if err := t.reconcileGhosts(host, r.Unmapped, source, snapshotID); err != nil {
			return err
		}
	}
	_, err = t.ExecContext(t.ctx, `UPDATE host SET current_snapshot_id = ?, degraded = ? WHERE host_id = ?`, snapshotID, degraded, host.id)
	return err
}

// placeDrive extends the drive's open placement on this host if it still
// matches, or closes whatever placement it had and opens a new one, with
// the event that explains the change.
func (t *tx) placeDrive(host *hostRow, row devRow, here *placementRow, source string, snapshotID int64) error {
	d := row.dev
	key, via := enclosureKey(d), enclosureVia(d)
	if here != nil && here.enclosure == key && here.bay == d.Bay && here.uses == row.uses {
		_, err := t.ExecContext(t.ctx, `UPDATE placement SET last_seen = ?, dev_name = ?, enclosure_via = ? WHERE placement_id = ?`, t.obs, d.DevName, via, here.id)
		return err
	}
	// Uses go in as the canonical JSON so live ingest and Rebuild write the
	// same detail.
	slot := map[string]any{"enclosure": key, "enclosure_via": via, "bay": d.Bay, "uses": json.RawMessage(row.uses), "dev_name": d.DevName}

	prev, err := t.openPlacementAnywhere(row.driveID)
	if err != nil {
		return err
	}
	switch {
	case prev != nil:
		reason, kind := EndMoved, EventMovedBay
		switch {
		case prev.hostID != host.id:
			kind = EventMovedHost
		case prev.enclosure == key && prev.bay == d.Bay:
			reason, kind = EndUseChanged, EventUseChanged
		}
		if err := t.closePlacement(prev.id, reason); err != nil {
			return err
		}
		detail := map[string]any{"from_host": prev.hostname, "from_enclosure": prev.enclosure, "from_enclosure_via": prev.enclosureVia, "from_bay": prev.bay, "from_uses": json.RawMessage(prev.uses)}
		for k, v := range slot {
			detail["to_"+k] = v
		}
		if err := t.event(kind, row.driveID, host.id, detail, source, snapshotID); err != nil {
			return err
		}
	case row.created:
		if err := t.event(EventFirstSeen, row.driveID, host.id, slot, source, snapshotID); err != nil {
			return err
		}
	default:
		last, err := t.lastPlacement(row.driveID)
		if err != nil {
			return err
		}
		kind := EventAppeared
		if last != nil && last.endReason == EndVanished {
			kind = EventReappeared
			slot["gap_secs"] = t.obs - last.lastSeen
			slot["from_host"] = last.hostname
			slot["from_enclosure"] = last.enclosure
			slot["from_enclosure_via"] = last.enclosureVia
			slot["from_bay"] = last.bay
			slot["same_slot"] = last.hostID == host.id && last.enclosure == key && last.bay == d.Bay
		}
		if err := t.event(kind, row.driveID, host.id, slot, source, snapshotID); err != nil {
			return err
		}
	}
	_, err = t.ExecContext(t.ctx, `INSERT INTO placement (drive_id, host_id, enclosure, enclosure_via, bay, uses, dev_name, first_seen, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.driveID, host.id, key, via, d.Bay, row.uses, d.DevName, t.obs, t.obs)
	return err
}

// renameEnclosures spots an enclosure whose key changed under every
// drive in it while each drive kept its bay: the kernel numbered the
// host's SAS controllers differently after a boot, or an upgraded agent
// started reporting SES enclosure identifiers instead of expander
// addresses, or reporting the HBA's own bays at all (the old key is then
// ""). That is one enclosure with a new name, not a set of moves, so the
// open placements are updated in place (the ones for drives absent from
// this report too; they sit in the same enclosure), a name given to the
// old key follows it, and one host-level event records it. A single
// drive is not enough evidence; it may really have moved to another
// enclosure's same bay.
func (t *tx) renameEnclosures(host *hostRow, rows []devRow, byDrive map[int64]*placementRow, source string, snapshotID int64) error {
	type target struct{ key, via string }
	moved := map[string]map[target]int{}
	unchanged := map[string]bool{} // some drive still reports the old key, or moved to another bay
	for _, row := range rows {
		if row.driveID == 0 {
			continue
		}
		p := byDrive[row.driveID]
		if p == nil || p.bay == "" {
			continue
		}
		key := enclosureKey(row.dev)
		if key == p.enclosure || key == "" || row.dev.Bay != p.bay {
			unchanged[p.enclosure] = true
			continue
		}
		if moved[p.enclosure] == nil {
			moved[p.enclosure] = map[target]int{}
		}
		moved[p.enclosure][target{key, enclosureVia(row.dev)}]++
	}
	for old, targets := range moved {
		if unchanged[old] || len(targets) != 1 {
			continue
		}
		for to, n := range targets {
			if n < 2 {
				continue
			}
			if _, err := t.ExecContext(t.ctx, `UPDATE placement SET enclosure = ?, enclosure_via = ? WHERE host_id = ? AND enclosure = ? AND ended_at IS NULL`, to.key, to.via, host.id, old); err != nil {
				return err
			}
			if old != "" {
				if _, err := t.ExecContext(t.ctx, `UPDATE enclosure_name SET enclosure = ?1 WHERE enclosure = ?2 AND NOT EXISTS (SELECT 1 FROM enclosure_name WHERE enclosure = ?1)`, to.key, old); err != nil {
					return err
				}
				if _, err := t.ExecContext(t.ctx, `DELETE FROM enclosure WHERE host_id = ? AND enclosure = ?`, host.id, old); err != nil {
					return err
				}
			}
			var oldVia string
			for _, p := range byDrive {
				if p.enclosure == old {
					oldVia = p.enclosureVia
					p.enclosure, p.enclosureVia = to.key, to.via
				}
			}
			detail := map[string]any{"from": old, "from_via": oldVia, "to": to.key, "to_via": to.via, "drives": n}
			if err := t.event(EventEnclosureRenamed, 0, host.id, detail, source, snapshotID); err != nil {
				return err
			}
		}
	}
	return nil
}

// noteEnclosures records every enclosure the report mentions, through a
// drive or an empty bay, with how many bays were seen in it, so an empty
// shelf can be listed and named before anything sits in it.
func (t *tx) noteEnclosures(host *hostRow, r Report, rows []devRow) error {
	type enc struct {
		via, viaID, product, board string
		bays                       map[string]bool
	}
	seen := map[string]*enc{}
	note := func(key, via, viaID, product, board, bay string) {
		if key == "" {
			return
		}
		e := seen[key]
		if e == nil {
			e = &enc{via: via, viaID: viaID, bays: map[string]bool{}}
			seen[key] = e
		}
		if product != "" {
			e.product = product
		}
		if board != "" {
			e.board = board
		}
		if bay != "" {
			e.bays[bay] = true
		}
	}
	for _, row := range rows {
		d := row.dev
		if d.Error != "" && enclosureKey(d) == "" {
			continue
		}
		viaID := d.EnclosureViaID
		if viaID == "" {
			viaID = d.ExpanderID
		}
		note(enclosureKey(d), enclosureVia(d), viaID, d.EnclosureModel, d.EnclosureBoard, d.Bay)
	}
	for _, b := range r.EmptyBays {
		note(enclosureKey(ReportDevice{EnclosureID: b.EnclosureID, EnclosureVia: b.EnclosureVia, EnclosureViaID: b.EnclosureViaID}), b.EnclosureVia, b.EnclosureViaID, b.EnclosureModel, b.EnclosureBoard, b.Bay)
	}
	for key, e := range seen {
		if _, err := t.ExecContext(t.ctx, `
			INSERT INTO enclosure (host_id, enclosure, via, via_address, product, board, bays, first_seen, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (host_id, enclosure) DO UPDATE SET via = excluded.via, via_address = CASE WHEN excluded.via_address != '' THEN excluded.via_address ELSE via_address END,
				product = CASE WHEN excluded.product != '' THEN excluded.product ELSE product END, board = CASE WHEN excluded.board != '' THEN excluded.board ELSE board END,
				bays = MAX(bays, excluded.bays), last_seen = excluded.last_seen`,
			host.id, key, e.via, e.viaID, e.product, e.board, len(e.bays), t.obs, t.obs); err != nil {
			return err
		}
	}
	return nil
}

// vanish closes a placement the host no longer reports. If a pool on the
// host still references the drive, the event says so: the drive is
// physically there as far as ZFS knows.
func (t *tx) vanish(p *placementRow, unmapped []PoolMember, source string, snapshotID int64) error {
	if err := t.closePlacement(p.id, EndVanished); err != nil {
		return err
	}
	detail := map[string]any{"last_seen": p.lastSeen, "enclosure": p.enclosure, "enclosure_via": p.enclosureVia, "bay": p.bay, "uses": json.RawMessage(p.uses), "dev_name": p.devName}
	for _, m := range unmapped {
		id, err := t.driveForPath(m.Path)
		if err != nil {
			return err
		}
		if id == p.driveID {
			detail["still_in_pool"] = m.Pool
			detail["pool_state"] = m.State
			break
		}
	}
	return t.event(EventVanished, p.driveID, p.hostID, detail, source, snapshotID)
}

func (t *tx) closePlacement(id int64, reason string) error {
	_, err := t.ExecContext(t.ctx, `UPDATE placement SET ended_at = ?, end_reason = ? WHERE placement_id = ?`, t.obs, reason, id)
	return err
}

// reconcileGhosts keeps one open ghost per (pool, path) the host reports as
// unmapped, closing the ones it stopped reporting.
func (t *tx) reconcileGhosts(host *hostRow, unmapped []PoolMember, source string, snapshotID int64) error {
	rows, err := t.QueryContext(t.ctx, `SELECT ghost_id, pool, member_path FROM ghost WHERE host_id = ? AND ended_at IS NULL`, host.id)
	if err != nil {
		return err
	}
	open := map[string]int64{}
	for rows.Next() {
		var id int64
		var pool, p string
		if err := rows.Scan(&id, &pool, &p); err != nil {
			rows.Close()
			return err
		}
		open[pool+"\x00"+p] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range unmapped {
		k := m.Pool + "\x00" + m.Path
		if id, ok := open[k]; ok {
			delete(open, k)
			if _, err := t.ExecContext(t.ctx, `UPDATE ghost SET last_seen = ?, member_state = ?, member_guid = ? WHERE ghost_id = ?`, t.obs, m.State, m.GUID, id); err != nil {
				return err
			}
			continue
		}
		driveID, err := t.driveForPath(m.Path)
		if err != nil {
			return err
		}
		if _, err := t.ExecContext(t.ctx, `INSERT INTO ghost (host_id, pool, member_path, member_guid, member_state, drive_id, first_seen, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			host.id, m.Pool, m.Path, m.GUID, m.State, nullID(driveID), t.obs, t.obs); err != nil {
			return err
		}
		if err := t.event(EventPoolMissingMember, driveID, host.id, map[string]any{"pool": m.Pool, "path": m.Path, "guid": m.GUID, "state": m.State}, source, snapshotID); err != nil {
			return err
		}
	}
	for _, id := range open {
		if _, err := t.ExecContext(t.ctx, `UPDATE ghost SET ended_at = ? WHERE ghost_id = ?`, t.obs, id); err != nil {
			return err
		}
	}
	return nil
}

const placementColumns = `p.placement_id, p.drive_id, p.host_id, h.hostname, p.enclosure, p.enclosure_via, p.bay, p.uses, p.dev_name, p.first_seen, p.last_seen, p.end_reason`

func scanPlacement(row interface{ Scan(...any) error }) (*placementRow, error) {
	p := &placementRow{}
	err := row.Scan(&p.id, &p.driveID, &p.hostID, &p.hostname, &p.enclosure, &p.enclosureVia, &p.bay, &p.uses, &p.devName, &p.firstSeen, &p.lastSeen, &p.endReason)
	return p, err
}

func (t *tx) openPlacements(hostID int64) ([]*placementRow, error) {
	rows, err := t.QueryContext(t.ctx, `SELECT `+placementColumns+` FROM placement p JOIN host h USING (host_id) WHERE p.host_id = ? AND p.ended_at IS NULL`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*placementRow
	for rows.Next() {
		p, err := scanPlacement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (t *tx) openPlacementAnywhere(driveID int64) (*placementRow, error) {
	p, err := scanPlacement(t.QueryRowContext(t.ctx, `SELECT `+placementColumns+` FROM placement p JOIN host h USING (host_id) WHERE p.drive_id = ? AND p.ended_at IS NULL`, driveID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

func (t *tx) lastPlacement(driveID int64) (*placementRow, error) {
	p, err := scanPlacement(t.QueryRowContext(t.ctx, `SELECT `+placementColumns+` FROM placement p JOIN host h USING (host_id) WHERE p.drive_id = ? ORDER BY p.first_seen DESC, p.placement_id DESC LIMIT 1`, driveID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

// memberStates returns drive -> ZFS member state from a snapshot.
func (t *tx) memberStates(snapshotID sql.NullInt64) (map[int64]string, error) {
	states := map[int64]string{}
	if !snapshotID.Valid {
		return states, nil
	}
	rows, err := t.QueryContext(t.ctx, `SELECT drive_id, member_state FROM snapshot_device WHERE snapshot_id = ? AND drive_id IS NOT NULL`, snapshotID.Int64)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var state string
		if err := rows.Scan(&id, &state); err != nil {
			return nil, err
		}
		states[id] = state
	}
	return states, rows.Err()
}

// statuses returns the server's status for every identified drive in the
// report, for the agent's local cache.
func (t *tx) statuses(rows []devRow) ([]DriveStatus, error) {
	var out []DriveStatus
	for _, row := range rows {
		if row.driveID == 0 {
			continue
		}
		var status, detail string
		err := t.QueryRowContext(t.ctx, `
			SELECT d.status, COALESCE((SELECT e.detail FROM event e WHERE e.drive_id = d.drive_id AND e.kind = ? ORDER BY e.ts DESC, e.event_id DESC LIMIT 1), '{}')
			FROM drive d WHERE d.drive_id = ?`, EventStatusChanged, row.driveID).Scan(&status, &detail)
		if err != nil {
			return nil, err
		}
		var note struct{ Note string }
		_ = json.Unmarshal([]byte(detail), &note)
		out = append(out, DriveStatus{Identity: row.dev.Identity, Status: status, Note: note.Note})
	}
	return out, nil
}

// Sweep marks hosts stale when they have not reported for three intervals,
// recording one host_stale event each. Open placements on a stale host are
// left open: only a report from the host can vanish a drive.
func (s *Store) Sweep(ctx context.Context, interval time.Duration) (int, error) {
	now := s.now()
	cutoff := now.Add(-3 * interval).Unix()
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer sqlTx.Rollback()
	t := &tx{Tx: sqlTx, ctx: ctx, now: now.Unix(), obs: now.Unix()}
	rows, err := t.QueryContext(ctx, `SELECT host_id, last_report FROM host WHERE stale_since IS NULL AND last_report IS NOT NULL AND last_report < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	type stale struct {
		id, last int64
	}
	var found []stale
	for rows.Next() {
		var h stale
		if err := rows.Scan(&h.id, &h.last); err != nil {
			rows.Close()
			return 0, err
		}
		found = append(found, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, h := range found {
		if _, err := t.ExecContext(ctx, `UPDATE host SET stale_since = ? WHERE host_id = ?`, t.now, h.id); err != nil {
			return 0, err
		}
		if err := t.event(EventHostStale, 0, h.id, map[string]any{"last_report": h.last, "silent_secs": t.now - h.last}, "sweeper", 0); err != nil {
			return 0, err
		}
	}
	return len(found), sqlTx.Commit()
}
