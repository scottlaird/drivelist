package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// sasNodeRow is a sas_node row.
type sasNodeRow struct {
	SASNode
	firstSeen, lastSeen int64
	gone                sql.NullInt64
}

// sasPhyRow is a sas_phy row.
type sasPhyRow struct {
	SASPhy
	driveID             sql.NullInt64
	firstSeen, lastSeen int64
	gone                sql.NullInt64
}

func sasKey(owner string, phy int) string { return fmt.Sprintf("%s/%d", owner, phy) }

// ingestSAS folds the report's SAS topology into the host's sas_node and
// sas_phy rows and turns what changed into events: nodes appearing,
// vanishing or changing firmware; a phy's link rate changing; a phy's far
// end changing; a port's width changing; and counters growing, which
// also leaves a sample. A report that carries no topology at all (an
// older agent, or a host without SAS) changes nothing, so a downgrade
// does not read as every shelf vanishing.
func (t *tx) ingestSAS(host *hostRow, r Report, rows []devRow) error {
	if len(r.SASNodes) == 0 {
		return nil
	}
	driveOf := map[string]int64{}
	for _, row := range rows {
		if row.driveID != 0 {
			driveOf[row.dev.DevName] = row.driveID
		}
	}
	nodeName := map[string]string{}
	for _, n := range r.SASNodes {
		nodeName[n.Address] = n.Name
	}
	// The counters count since boot. The first report from a new boot
	// restarts them: no growth is derived from it, whatever the previous
	// boot's totals were. Agents that send no boot id get the old test,
	// counters below the last report.
	fresh := false
	if r.Host.BootID != "" {
		var sasBoot string
		if err := t.QueryRowContext(t.ctx, `SELECT sas_boot_id FROM host WHERE host_id = ?`, host.id).Scan(&sasBoot); err != nil {
			return err
		}
		if sasBoot != r.Host.BootID {
			fresh = sasBoot != ""
			if _, err := t.ExecContext(t.ctx, `UPDATE host SET sas_boot_id = ? WHERE host_id = ?`, r.Host.BootID, host.id); err != nil {
				return err
			}
		}
	}

	// Nodes.
	prevNodes, err := t.sasNodes(host.id)
	if err != nil {
		return err
	}
	reported := map[string]bool{}
	for _, n := range r.SASNodes {
		reported[n.Address] = true
		prev, ok := prevNodes[n.Address]
		detail := map[string]any{"kind": n.Kind, "name": n.Name, "address": n.Address, "product": n.Vendor + " " + n.Product, "revision": n.Revision}
		switch {
		case !ok || prev.gone.Valid:
			detail["change"] = "appeared"
			if err := t.event(EventSASNodeChanged, 0, host.id, detail, "report", 0); err != nil {
				return err
			}
		case prev.Revision != n.Revision && prev.Revision != "" && n.Revision != "":
			detail["change"], detail["from"], detail["to"] = "revision", prev.Revision, n.Revision
			if err := t.event(EventSASNodeChanged, 0, host.id, detail, "report", 0); err != nil {
				return err
			}
		}
		first := t.obs
		if ok && !prev.gone.Valid {
			first = prev.firstSeen
		}
		if _, err := t.ExecContext(t.ctx, `
			INSERT INTO sas_node (host_id, address, kind, name, vendor, product, revision, parent_address, upstream_port, first_seen, last_seen, gone_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
			ON CONFLICT (host_id, address) DO UPDATE SET kind = excluded.kind, name = excluded.name, vendor = excluded.vendor, product = excluded.product,
				revision = excluded.revision, parent_address = excluded.parent_address, upstream_port = excluded.upstream_port,
				first_seen = excluded.first_seen, last_seen = excluded.last_seen, gone_at = NULL`,
			host.id, n.Address, n.Kind, n.Name, n.Vendor, n.Product, n.Revision, n.ParentAddress, n.UpstreamPort, first, t.obs); err != nil {
			return err
		}
	}
	for addr, prev := range prevNodes {
		if reported[addr] || prev.gone.Valid {
			continue
		}
		if _, err := t.ExecContext(t.ctx, `UPDATE sas_node SET gone_at = ? WHERE host_id = ? AND address = ?`, t.obs, host.id, addr); err != nil {
			return err
		}
		if _, err := t.ExecContext(t.ctx, `UPDATE sas_phy SET gone_at = ? WHERE host_id = ? AND owner_address = ? AND gone_at IS NULL`, t.obs, host.id, addr); err != nil {
			return err
		}
		detail := map[string]any{"change": "vanished", "kind": prev.Kind, "name": prev.Name, "address": addr, "product": prev.Vendor + " " + prev.Product, "revision": prev.Revision, "last_seen": prev.lastSeen}
		if err := t.event(EventSASNodeChanged, 0, host.id, detail, "report", 0); err != nil {
			return err
		}
	}

	// Phys.
	prevPhys, err := t.sasPhys(host.id)
	if err != nil {
		return err
	}
	widthBefore := map[string]int{}
	for _, p := range prevPhys {
		if p.Port != "" && !p.gone.Valid {
			widthBefore[sasKey(p.OwnerAddress, 0)+p.Port]++
		}
	}
	widthNow := map[string]int{}
	portAttached := map[string]SASPhy{}
	seenPhys := map[string]bool{}
	for _, p := range r.SASPhys {
		if p.Port != "" {
			widthNow[sasKey(p.OwnerAddress, 0)+p.Port]++
			portAttached[sasKey(p.OwnerAddress, 0)+p.Port] = p
		}
	}
	for _, p := range r.SASPhys {
		key := sasKey(p.OwnerAddress, p.PhyID)
		seenPhys[key] = true
		var driveID int64
		if p.DevName != "" {
			driveID = driveOf[p.DevName]
		}
		prev, ok := prevPhys[key]
		where := map[string]any{"owner": p.OwnerAddress, "owner_name": nodeName[p.OwnerAddress], "phy": p.PhyID, "port": p.Port, "attached_kind": p.AttachedKind, "attached": p.Attached, "dev_name": p.DevName, "bay": p.Bay}
		if ok && !prev.gone.Valid {
			// The drive to pin an event on: the one there now, else the one
			// that was there when the link went away.
			eventDrive := driveID
			if eventDrive == 0 && prev.driveID.Valid {
				eventDrive = prev.driveID.Int64
			}
			if prev.Rate != p.Rate {
				d := clone(where)
				d["from"], d["to"], d["from_gbit"], d["to_gbit"] = prev.Rate, p.Rate, prev.RateGbit, p.RateGbit
				if err := t.event(EventSASLinkChanged, eventDrive, host.id, d, "report", 0); err != nil {
					return err
				}
			}
			if prev.AttachedAddress != p.AttachedAddress && prev.AttachedAddress != "" && p.AttachedAddress != "" {
				d := clone(where)
				d["from_attached"], d["to_attached"], d["from_address"], d["to_address"], d["from_dev_name"], d["to_dev_name"] = prev.Attached, p.Attached, prev.AttachedAddress, p.AttachedAddress, prev.DevName, p.DevName
				if err := t.event(EventSASAttachedChanged, eventDrive, host.id, d, "report", 0); err != nil {
					return err
				}
			}
			if !fresh && p.InvalidDword >= prev.InvalidDword && p.DisparityError >= prev.DisparityError && p.LossDwordSync >= prev.LossDwordSync && p.PhyResetProblem >= prev.PhyResetProblem {
				grew := SASPhy{InvalidDword: p.InvalidDword - prev.InvalidDword, DisparityError: p.DisparityError - prev.DisparityError,
					LossDwordSync: p.LossDwordSync - prev.LossDwordSync, PhyResetProblem: p.PhyResetProblem - prev.PhyResetProblem}
				if grew.InvalidDword+grew.DisparityError+grew.LossDwordSync+grew.PhyResetProblem > 0 {
					if _, err := t.ExecContext(t.ctx, `INSERT OR REPLACE INTO sas_phy_sample (host_id, owner_address, phy_id, ts, drive_id, invalid_dword, disparity_error, loss_dword_sync, phy_reset_problem) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
						host.id, p.OwnerAddress, p.PhyID, t.obs, nullID(eventDrive), grew.InvalidDword, grew.DisparityError, grew.LossDwordSync, grew.PhyResetProblem); err != nil {
						return err
					}
					if err := t.sasErrorsWarning(host.id, eventDrive, where, grew, p); err != nil {
						return err
					}
				}
			}
			// Counters below the last report: the host rebooted, or the
			// counter was reset. Nothing to derive; the totals just restart.
		}
		first := t.obs
		if ok && !prev.gone.Valid {
			first = prev.firstSeen
		}
		if _, err := t.ExecContext(t.ctx, `
			INSERT INTO sas_phy (host_id, owner_address, phy_id, name, port, port_width, rate, rate_gbit, attached_kind, attached, attached_address, dev_name, bay, drive_id, enabled,
				invalid_dword, disparity_error, loss_dword_sync, phy_reset_problem, first_seen, last_seen, gone_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
			ON CONFLICT (host_id, owner_address, phy_id) DO UPDATE SET name = excluded.name, port = excluded.port, port_width = excluded.port_width, rate = excluded.rate, rate_gbit = excluded.rate_gbit,
				attached_kind = excluded.attached_kind, attached = excluded.attached, attached_address = excluded.attached_address, dev_name = excluded.dev_name, bay = excluded.bay,
				drive_id = excluded.drive_id, enabled = excluded.enabled, invalid_dword = excluded.invalid_dword, disparity_error = excluded.disparity_error,
				loss_dword_sync = excluded.loss_dword_sync, phy_reset_problem = excluded.phy_reset_problem, first_seen = excluded.first_seen, last_seen = excluded.last_seen, gone_at = NULL`,
			host.id, p.OwnerAddress, p.PhyID, p.Name, p.Port, p.PortWidth, p.Rate, p.RateGbit, p.AttachedKind, p.Attached, p.AttachedAddress, p.DevName, p.Bay, nullID(driveID), p.Enabled,
			p.InvalidDword, p.DisparityError, p.LossDwordSync, p.PhyResetProblem, first, t.obs); err != nil {
			return err
		}
	}
	// Phys of reported nodes that the report no longer lists.
	for key, prev := range prevPhys {
		if seenPhys[key] || prev.gone.Valid || !reported[prev.OwnerAddress] {
			continue
		}
		if _, err := t.ExecContext(t.ctx, `UPDATE sas_phy SET gone_at = ? WHERE host_id = ? AND owner_address = ? AND phy_id = ?`, t.obs, host.id, prev.OwnerAddress, prev.PhyID); err != nil {
			return err
		}
	}
	// Port widths, for ports that existed before and still do.
	for k, before := range widthBefore {
		now, ok := widthNow[k]
		if !ok || now == before {
			continue
		}
		p := portAttached[k]
		var driveID int64
		if p.DevName != "" {
			driveID = driveOf[p.DevName]
		}
		d := map[string]any{"owner": p.OwnerAddress, "owner_name": nodeName[p.OwnerAddress], "port": p.Port, "from": before, "to": now, "attached_kind": p.AttachedKind, "attached": p.Attached, "dev_name": p.DevName}
		if err := t.event(EventSASPortChanged, driveID, host.id, d, "report", 0); err != nil {
			return err
		}
	}
	return nil
}

func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+6)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// sasErrorsWarning records one sas_errors event per phy and UTC day, with
// the growth that triggered it and the totals since boot.
func (t *tx) sasErrorsWarning(hostID, driveID int64, where map[string]any, grew, total SASPhy) error {
	day := time.Unix(t.obs, 0).UTC().Truncate(24 * time.Hour)
	var n int
	if err := t.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM event WHERE kind = ? AND host_id = ? AND ts >= ? AND ts < ? AND detail LIKE ?`,
		EventSASErrors, hostID, day.Unix(), day.Add(24*time.Hour).Unix(), fmt.Sprintf(`%%"owner":"%s"%%"phy":%v,%%`, where["owner"], where["phy"])).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	d := clone(where)
	d["grew"] = map[string]uint64{"invalid_dword": grew.InvalidDword, "disparity_error": grew.DisparityError, "loss_dword_sync": grew.LossDwordSync, "phy_reset_problem": grew.PhyResetProblem}
	d["total"] = map[string]uint64{"invalid_dword": total.InvalidDword, "disparity_error": total.DisparityError, "loss_dword_sync": total.LossDwordSync, "phy_reset_problem": total.PhyResetProblem}
	return t.event(EventSASErrors, driveID, hostID, d, "report", 0)
}

const sasNodeColumns = `n.address, n.kind, n.name, n.vendor, n.product, n.revision, n.parent_address, n.upstream_port, n.first_seen, n.last_seen, n.gone_at`
const sasPhyColumns = `p.owner_address, p.phy_id, p.name, p.port, p.port_width, p.rate, p.rate_gbit, p.attached_kind, p.attached, p.attached_address, p.dev_name, p.bay, p.drive_id, p.enabled, p.invalid_dword, p.disparity_error, p.loss_dword_sync, p.phy_reset_problem, p.first_seen, p.last_seen, p.gone_at`

func scanSASNode(rows interface{ Scan(...any) error }) (sasNodeRow, error) {
	var n sasNodeRow
	err := rows.Scan(&n.Address, &n.Kind, &n.Name, &n.Vendor, &n.Product, &n.Revision, &n.ParentAddress, &n.UpstreamPort, &n.firstSeen, &n.lastSeen, &n.gone)
	return n, err
}

func scanSASPhy(rows interface{ Scan(...any) error }) (sasPhyRow, error) {
	var p sasPhyRow
	err := rows.Scan(&p.OwnerAddress, &p.PhyID, &p.Name, &p.Port, &p.PortWidth, &p.Rate, &p.RateGbit, &p.AttachedKind, &p.Attached, &p.AttachedAddress, &p.DevName, &p.Bay, &p.driveID, &p.Enabled,
		&p.InvalidDword, &p.DisparityError, &p.LossDwordSync, &p.PhyResetProblem, &p.firstSeen, &p.lastSeen, &p.gone)
	return p, err
}

func (t *tx) sasNodes(hostID int64) (map[string]sasNodeRow, error) {
	rows, err := t.QueryContext(t.ctx, `SELECT `+sasNodeColumns+` FROM sas_node n WHERE n.host_id = ?`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]sasNodeRow{}
	for rows.Next() {
		n, err := scanSASNode(rows)
		if err != nil {
			return nil, err
		}
		out[n.Address] = n
	}
	return out, rows.Err()
}

func (t *tx) sasPhys(hostID int64) (map[string]sasPhyRow, error) {
	rows, err := t.QueryContext(t.ctx, `SELECT `+sasPhyColumns+` FROM sas_phy p WHERE p.host_id = ?`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]sasPhyRow{}
	for rows.Next() {
		p, err := scanSASPhy(rows)
		if err != nil {
			return nil, err
		}
		out[sasKey(p.OwnerAddress, p.PhyID)] = p
	}
	return out, rows.Err()
}

// SASNodeState and SASPhyState are what queries return: the reported
// values plus when the server first and last saw them.
type SASNodeState struct {
	SASNode
	Hostname  string
	FirstSeen time.Time
	LastSeen  time.Time
	GoneAt    time.Time // zero while present
}

type SASPhyState struct {
	SASPhy
	Hostname  string
	OwnerName string
	Serial    string // of the drive behind it
	FirstSeen time.Time
	LastSeen  time.Time
	GoneAt    time.Time
}

// SASState returns a host's topology as last reported, present nodes and
// phys first, gone ones after (kept so a vanished shelf can be read).
func (s *Store) SASState(ctx context.Context, host string) ([]SASNodeState, []SASPhyState, error) {
	h, err := s.ResolveHost(ctx, host)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+sasNodeColumns+` FROM sas_node n WHERE n.host_id = ? ORDER BY n.gone_at IS NOT NULL, n.kind DESC, n.name`, h.ID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var nodes []SASNodeState
	for rows.Next() {
		n, err := scanSASNode(rows)
		if err != nil {
			return nil, nil, err
		}
		nodes = append(nodes, SASNodeState{SASNode: n.SASNode, Hostname: h.Hostname, FirstSeen: time.Unix(n.firstSeen, 0).UTC(), LastSeen: time.Unix(n.lastSeen, 0).UTC(), GoneAt: unix(n.gone)})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	nameOf := map[string]string{}
	for _, n := range nodes {
		nameOf[n.Address] = n.Name
	}
	prow, err := s.db.QueryContext(ctx, `SELECT `+sasPhyColumns+`, COALESCE(d.serial, '') FROM sas_phy p LEFT JOIN drive d USING (drive_id) WHERE p.host_id = ? ORDER BY p.gone_at IS NOT NULL, p.owner_address, p.phy_id`, h.ID)
	if err != nil {
		return nil, nil, err
	}
	defer prow.Close()
	var phys []SASPhyState
	for prow.Next() {
		var p sasPhyRow
		var serial string
		if err := prow.Scan(&p.OwnerAddress, &p.PhyID, &p.Name, &p.Port, &p.PortWidth, &p.Rate, &p.RateGbit, &p.AttachedKind, &p.Attached, &p.AttachedAddress, &p.DevName, &p.Bay, &p.driveID, &p.Enabled,
			&p.InvalidDword, &p.DisparityError, &p.LossDwordSync, &p.PhyResetProblem, &p.firstSeen, &p.lastSeen, &p.gone, &serial); err != nil {
			return nil, nil, err
		}
		phys = append(phys, SASPhyState{SASPhy: p.SASPhy, Hostname: h.Hostname, OwnerName: nameOf[p.OwnerAddress], Serial: serial,
			FirstSeen: time.Unix(p.firstSeen, 0).UTC(), LastSeen: time.Unix(p.lastSeen, 0).UTC(), GoneAt: unix(p.gone)})
	}
	return nodes, phys, prow.Err()
}

// SASErrorRow is one phy's counter growth over a window.
type SASErrorRow struct {
	Hostname        string
	OwnerName       string
	OwnerAddress    string
	PhyID           int
	Port            string
	AttachedKind    string
	Attached        string
	DevName         string
	Serial          string
	Bay             string
	InvalidDword    uint64
	DisparityError  uint64
	LossDwordSync   uint64
	PhyResetProblem uint64
	LastAt          time.Time
	Samples         int
}

// SASErrors sums each phy's growth since a time, on one host or all,
// most growth first.
func (s *Store) SASErrors(ctx context.Context, host string, since time.Time) ([]SASErrorRow, error) {
	where := "s.ts >= ?"
	args := []any{since.Unix()}
	if host != "" {
		h, err := s.ResolveHost(ctx, host)
		if err != nil {
			return nil, err
		}
		where += " AND s.host_id = ?"
		args = append(args, h.ID)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.hostname, COALESCE(n.name, ''), s.owner_address, s.phy_id, COALESCE(p.port, ''), COALESCE(p.attached_kind, ''), COALESCE(p.attached, ''), COALESCE(p.dev_name, ''), COALESCE(d.serial, ''), COALESCE(p.bay, ''),
		       SUM(s.invalid_dword), SUM(s.disparity_error), SUM(s.loss_dword_sync), SUM(s.phy_reset_problem), MAX(s.ts), COUNT(*)
		FROM sas_phy_sample s JOIN host h USING (host_id)
		LEFT JOIN sas_phy p ON p.host_id = s.host_id AND p.owner_address = s.owner_address AND p.phy_id = s.phy_id
		LEFT JOIN sas_node n ON n.host_id = s.host_id AND n.address = s.owner_address
		LEFT JOIN drive d ON d.drive_id = p.drive_id
		WHERE `+where+`
		GROUP BY s.host_id, s.owner_address, s.phy_id
		ORDER BY SUM(s.invalid_dword + s.disparity_error + s.loss_dword_sync + s.phy_reset_problem) DESC, h.hostname, s.owner_address, s.phy_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SASErrorRow
	for rows.Next() {
		var r SASErrorRow
		var last int64
		if err := rows.Scan(&r.Hostname, &r.OwnerName, &r.OwnerAddress, &r.PhyID, &r.Port, &r.AttachedKind, &r.Attached, &r.DevName, &r.Serial, &r.Bay,
			&r.InvalidDword, &r.DisparityError, &r.LossDwordSync, &r.PhyResetProblem, &last, &r.Samples); err != nil {
			return nil, err
		}
		r.LastAt = time.Unix(last, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// SASPhyTotals returns every present phy's cumulative counters with the
// labels /metrics needs. Phys that are down and have never counted an
// error are left out to keep the series list to what matters.
func (s *Store) SASPhyTotals(ctx context.Context) ([]SASPhyState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.hostname, COALESCE(n.name, ''), p.owner_address, p.phy_id, p.port, p.rate_gbit, p.attached_kind, p.attached, p.dev_name, p.invalid_dword, p.disparity_error, p.loss_dword_sync, p.phy_reset_problem
		FROM sas_phy p JOIN host h USING (host_id) LEFT JOIN sas_node n ON n.host_id = p.host_id AND n.address = p.owner_address
		WHERE p.gone_at IS NULL AND (p.rate_gbit > 0 OR p.invalid_dword + p.disparity_error + p.loss_dword_sync + p.phy_reset_problem > 0)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SASPhyState
	for rows.Next() {
		var p SASPhyState
		if err := rows.Scan(&p.Hostname, &p.OwnerName, &p.OwnerAddress, &p.PhyID, &p.Port, &p.RateGbit, &p.AttachedKind, &p.Attached, &p.DevName, &p.InvalidDword, &p.DisparityError, &p.LossDwordSync, &p.PhyResetProblem); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
