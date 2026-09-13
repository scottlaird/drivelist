package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// derivedEvents are the kinds Rebuild recomputes; every other kind (manual
// annotations, merges, sweeper and sampler events) is kept.
var derivedEvents = []string{
	EventFirstSeen, EventAppeared, EventVanished, EventReappeared, EventMovedHost, EventMovedBay, EventExpanderRenamed,
	EventUseChanged, EventMemberStateChanged, EventReportDegraded,
}

// RebuildResult counts what Rebuild produced.
type RebuildResult struct {
	Snapshots  int
	Placements int
	Events     int
}

// Rebuild throws away every placement and every derived event and
// recomputes them by replaying the stored snapshots host by host in order,
// through the same diff Ingest uses. It exists so a fix to the ingest
// logic can be applied to history, and as the proof that placements are a
// pure function of the snapshots. Ghosts, drives, keys, annotations and
// samples are untouched. The whole rebuild is one transaction.
func (s *Store) Rebuild(ctx context.Context) (RebuildResult, error) {
	now := s.now().Unix()
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RebuildResult{}, err
	}
	defer sqlTx.Rollback()
	t := &tx{Tx: sqlTx, ctx: ctx, now: now, obs: now}

	kinds := "'" + strings.Join(derivedEvents, "','") + "'"
	for _, q := range []string{
		`DELETE FROM placement`,
		`DELETE FROM event WHERE kind IN (` + kinds + `)`,
		`UPDATE host SET current_snapshot_id = NULL, degraded = 0`,
	} {
		if _, err := t.ExecContext(ctx, q); err != nil {
			return RebuildResult{}, err
		}
	}

	rows, err := t.QueryContext(ctx, `SELECT snapshot_id, host_id, first_at, last_at, errors FROM snapshot ORDER BY first_at, snapshot_id`)
	if err != nil {
		return RebuildResult{}, err
	}
	type snap struct {
		id, hostID, firstAt, lastAt int64
		errors                      string
	}
	var snaps []snap
	for rows.Next() {
		var sn snap
		if err := rows.Scan(&sn.id, &sn.hostID, &sn.firstAt, &sn.lastAt, &sn.errors); err != nil {
			rows.Close()
			return RebuildResult{}, err
		}
		snaps = append(snaps, sn)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return RebuildResult{}, err
	}

	var result RebuildResult
	for _, sn := range snaps {
		host, err := t.hostByID(sn.hostID)
		if err != nil {
			return RebuildResult{}, err
		}
		devRows, err := t.snapshotRows(sn.id)
		if err != nil {
			return RebuildResult{}, err
		}
		var collectorErrors []string
		json.Unmarshal([]byte(sn.errors), &collectorErrors)
		r := Report{Complete: len(collectorErrors) == 0, CollectorErrors: collectorErrors}
		t.obs = sn.firstAt
		if err := t.applySnapshot(host, r, devRows, "", sn.id); err != nil {
			return RebuildResult{}, err
		}
		// The heartbeats that confirmed this state until the next one, if
		// there were any: live, each one extended every open placement on
		// the host.
		if sn.lastAt > sn.firstAt {
			if _, err := t.ExecContext(ctx, `UPDATE placement SET last_seen = ? WHERE host_id = ? AND ended_at IS NULL`, sn.lastAt, sn.hostID); err != nil {
				return RebuildResult{}, err
			}
		}
		result.Snapshots++
	}
	if err := t.QueryRowContext(ctx, `SELECT COUNT(*) FROM placement`).Scan(&result.Placements); err != nil {
		return RebuildResult{}, err
	}
	if err := t.QueryRowContext(ctx, `SELECT COUNT(*) FROM event WHERE kind IN (`+kinds+`)`).Scan(&result.Events); err != nil {
		return RebuildResult{}, err
	}
	return result, sqlTx.Commit()
}

func (t *tx) hostByID(id int64) (*hostRow, error) {
	row := &hostRow{id: id}
	err := t.QueryRowContext(t.ctx, `SELECT hostname, last_observed, stale_since, degraded, current_snapshot_id FROM host WHERE host_id = ?`, id).
		Scan(&row.hostname, &row.lastObserved, &row.staleSince, &row.degraded, &row.currentSnapshotID)
	return row, err
}

// snapshotRows rebuilds the devRows a snapshot was applied from. A drive
// with no placement yet counts as newly created, which is what makes the
// replay emit first_seen where the original ingest did.
func (t *tx) snapshotRows(snapshotID int64) ([]devRow, error) {
	rows, err := t.QueryContext(t.ctx, `SELECT dev_name, drive_id, expander, expander_id, bay, enclosure_path, size_bytes, uses, member_state, dev_links, scsi_addr, error FROM snapshot_device WHERE snapshot_id = ? ORDER BY dev_name`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []devRow
	for rows.Next() {
		var d ReportDevice
		var driveID sql.NullInt64
		var uses, links string
		if err := rows.Scan(&d.DevName, &driveID, &d.Expander, &d.ExpanderID, &d.Bay, &d.EnclosurePath, &d.SizeBytes, &uses, &d.MemberState, &links, &d.SCSIAddr, &d.Error); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(uses), &d.Uses)
		json.Unmarshal([]byte(links), &d.DevLinks)
		row := devRow{dev: d, driveID: driveID.Int64, uses: uses}
		if driveID.Valid {
			var n int
			if err := t.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM placement WHERE drive_id = ?`, driveID.Int64).Scan(&n); err != nil {
				return nil, err
			}
			row.created = n == 0
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
