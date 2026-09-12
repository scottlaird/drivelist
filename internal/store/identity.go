package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// tx wraps a transaction with the two timestamps every write needs: the
// server clock and the agent's observed_at.
type tx struct {
	*sql.Tx
	ctx context.Context
	now int64 // server time
	obs int64 // agent observed_at
}

type hostRow struct {
	id                int64
	hostname          string
	lastObserved      sql.NullInt64
	staleSince        sql.NullInt64
	degraded          bool
	currentSnapshotID sql.NullInt64
	currentHash       string
	created           bool
}

// upsertHost finds or creates the host, updating its labels.
func (t *tx) upsertHost(h HostIdentity) (*hostRow, error) {
	if h.MachineID == "" {
		return nil, fmt.Errorf("report has no machine id")
	}
	row := &hostRow{hostname: h.Hostname}
	err := t.QueryRowContext(t.ctx, `
		SELECT h.host_id, h.last_observed, h.stale_since, h.degraded, h.current_snapshot_id, COALESCE(s.content_hash, '')
		FROM host h LEFT JOIN snapshot s ON s.snapshot_id = h.current_snapshot_id
		WHERE h.machine_id = ?`, h.MachineID).
		Scan(&row.id, &row.lastObserved, &row.staleSince, &row.degraded, &row.currentSnapshotID, &row.currentHash)
	switch {
	case err == sql.ErrNoRows:
		res, err := t.ExecContext(t.ctx, `INSERT INTO host (machine_id, hostname, os, agent_version, first_seen) VALUES (?, ?, ?, ?, ?)`,
			h.MachineID, h.Hostname, h.OS, h.AgentVersion, t.obs)
		if err != nil {
			return nil, err
		}
		row.id, _ = res.LastInsertId()
		row.created = true
		return row, t.event(EventHostFirstSeen, 0, row.id, map[string]any{"hostname": h.Hostname, "os": h.OS}, "report", 0)
	case err != nil:
		return nil, err
	}
	_, err = t.ExecContext(t.ctx, `UPDATE host SET hostname = ?, os = ?, agent_version = ? WHERE host_id = ?`, h.Hostname, h.OS, h.AgentVersion, row.id)
	return row, err
}

// resolveDrive finds the drive an identity belongs to, creating it if no
// key is known. It returns 0 for an identity with no keys. Two keys that
// point at different drives is an identity conflict: the lower id wins and
// an event records it once.
func (t *tx) resolveDrive(d ReportDevice) (id int64, created bool, err error) {
	keys := d.Identity.Keys()
	if len(keys) == 0 {
		return 0, false, nil
	}
	ids, err := t.drivesForKeys(keys)
	if err != nil {
		return 0, false, err
	}
	switch len(ids) {
	case 0:
		res, err := t.ExecContext(t.ctx, `INSERT INTO drive (wwn, vendor, model, serial, size_bytes, bus, first_seen, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			d.Identity.WWN, d.Identity.Vendor, d.Identity.Model, d.Identity.Serial, d.SizeBytes, d.Bus, t.obs, t.obs)
		if err != nil {
			return 0, false, err
		}
		id, _ = res.LastInsertId()
		created = true
	case 1:
		id = ids[0]
	default:
		id = ids[0]
		detail := map[string]any{"drives": ids, "keys": keys}
		dup, err := t.eventExists(EventIdentityConflict, id, detail)
		if err != nil {
			return 0, false, err
		}
		if !dup {
			if err := t.event(EventIdentityConflict, id, 0, detail, "report", 0); err != nil {
				return 0, false, err
			}
		}
		// Do not add keys under a conflict; a merge resolves it.
		return id, false, t.touchDrive(id, d)
	}
	for _, k := range keys {
		if _, err := t.ExecContext(t.ctx, `INSERT OR IGNORE INTO drive_key (key, drive_id) VALUES (?, ?)`, k, id); err != nil {
			return 0, false, err
		}
	}
	return id, created, t.touchDrive(id, d)
}

// drivesForKeys returns the distinct drives the keys point at, following
// merges, lowest id first.
func (t *tx) drivesForKeys(keys []string) ([]int64, error) {
	args := make([]any, len(keys))
	marks := make([]string, len(keys))
	for i, k := range keys {
		args[i] = k
		marks[i] = "?"
	}
	rows, err := t.QueryContext(t.ctx, `SELECT DISTINCT drive_id FROM drive_key WHERE key IN (`+strings.Join(marks, ",")+`) ORDER BY drive_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[int64]bool{}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		id, err = t.followMerge(id)
		if err != nil {
			return nil, err
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

func (t *tx) followMerge(id int64) (int64, error) {
	for range 16 {
		var into sql.NullInt64
		if err := t.QueryRowContext(t.ctx, `SELECT merged_into FROM drive WHERE drive_id = ?`, id).Scan(&into); err != nil {
			return 0, err
		}
		if !into.Valid {
			return id, nil
		}
		id = into.Int64
	}
	return 0, fmt.Errorf("drive %d: merge chain too deep", id)
}

// touchDrive records that the drive was seen and fills in identity fields
// that were empty the first time it was seen.
func (t *tx) touchDrive(id int64, d ReportDevice) error {
	_, err := t.ExecContext(t.ctx, `
		UPDATE drive SET
			last_seen = ?,
			size_bytes = CASE WHEN ? > 0 THEN ? ELSE size_bytes END,
			wwn = CASE WHEN wwn = '' THEN ? ELSE wwn END,
			vendor = CASE WHEN vendor = '' THEN ? ELSE vendor END,
			model = CASE WHEN model = '' THEN ? ELSE model END,
			serial = CASE WHEN serial = '' THEN ? ELSE serial END,
			bus = CASE WHEN bus = '' THEN ? ELSE bus END
		WHERE drive_id = ?`,
		t.obs, d.SizeBytes, d.SizeBytes, d.Identity.WWN, d.Identity.Vendor, d.Identity.Model, d.Identity.Serial, d.Bus, id)
	return err
}

// driveForPath resolves a pool member path such as
// /dev/disk/by-id/wwn-0x5000cca25492cd80-part1 to a known drive through its
// WWN key. Paths without a WWN form resolve to 0.
func (t *tx) driveForPath(p string) (int64, error) {
	base := path.Base(p)
	if i := strings.LastIndex(base, "-part"); i > 0 {
		base = base[:i]
	}
	var key string
	switch {
	case strings.HasPrefix(base, "wwn-"):
		key = "wwn:" + strings.TrimPrefix(base, "wwn-")
	case strings.HasPrefix(base, "scsi-3"):
		key = "wwn:0x" + strings.TrimPrefix(base, "scsi-3")
	case strings.HasPrefix(base, "nvme-eui."):
		key = "wwn:" + strings.TrimPrefix(base, "nvme-")
	default:
		return 0, nil
	}
	ids, err := t.drivesForKeys([]string{key})
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	return ids[0], nil
}

// event appends to the ledger. driveID and hostID of 0 mean none.
func (t *tx) event(kind string, driveID, hostID int64, detail map[string]any, source string, snapshotID int64) error {
	body, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	if detail == nil {
		body = []byte("{}")
	}
	_, err = t.ExecContext(t.ctx, `INSERT INTO event (ts, kind, drive_id, host_id, detail, source, snapshot_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.obs, kind, nullID(driveID), nullID(hostID), string(body), source, nullID(snapshotID))
	return err
}

func (t *tx) eventExists(kind string, driveID int64, detail map[string]any) (bool, error) {
	body, err := json.Marshal(detail)
	if err != nil {
		return false, err
	}
	var n int
	err = t.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM event WHERE kind = ? AND drive_id = ? AND detail = ?`, kind, driveID, string(body)).Scan(&n)
	return n > 0, err
}

func nullID(id int64) sql.NullInt64 {
	return sql.NullInt64{Int64: id, Valid: id != 0}
}
