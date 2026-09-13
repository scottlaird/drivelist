package store

import (
	"context"
	"errors"
	"fmt"
)

// Tables that reference a drive, and whether their primary key includes
// the drive (a move can then collide with a row the target already has,
// which is dropped: the two rows describe the same moment of the same
// physical drive).
var driveTables = []struct {
	name  string
	keyed bool
}{
	{"drive_key", false}, {"placement", false}, {"event", false}, {"snapshot_device", false}, {"ghost", false},
	{"kmsg_sample", true}, {"smart_sample", true}, {"smart_raw", false}, {"io_sample", true}, {"io_daily", true},
}

// MergeDrives folds drive from into drive into: every key, placement, event
// and sample moves to into, from is marked merged so its old references
// still resolve, and a merged event records it. If both were open
// somewhere, the placement confirmed less recently closes with reason
// merged. Use it when one physical drive got two records, typically from
// being seen once without its WWN.
func (s *Store) MergeDrives(ctx context.Context, intoRef, fromRef, actor string) (Event, error) {
	into, err := s.ResolveDrive(ctx, intoRef)
	if err != nil {
		return Event{}, err
	}
	from, err := s.ResolveDrive(ctx, fromRef)
	if err != nil {
		return Event{}, err
	}
	if into == from {
		return Event{}, errors.New("both references resolve to the same drive")
	}
	now := s.now().Unix()
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, err
	}
	defer sqlTx.Rollback()
	t := &tx{Tx: sqlTx, ctx: ctx, now: now, obs: now}

	var fromSerial, fromWWN, fromModel string
	if err := t.QueryRowContext(ctx, `SELECT serial, wwn, model FROM drive WHERE drive_id = ?`, from).Scan(&fromSerial, &fromWWN, &fromModel); err != nil {
		return Event{}, err
	}
	for _, tb := range driveTables {
		if tb.keyed {
			if _, err := t.ExecContext(ctx, `UPDATE OR IGNORE `+tb.name+` SET drive_id = ? WHERE drive_id = ?`, into, from); err != nil {
				return Event{}, err
			}
			if _, err := t.ExecContext(ctx, `DELETE FROM `+tb.name+` WHERE drive_id = ?`, from); err != nil {
				return Event{}, err
			}
			continue
		}
		if _, err := t.ExecContext(ctx, `UPDATE `+tb.name+` SET drive_id = ? WHERE drive_id = ?`, into, from); err != nil {
			return Event{}, err
		}
	}
	if err := t.closeDuplicateOpenPlacements(into); err != nil {
		return Event{}, err
	}
	if _, err := t.ExecContext(ctx, `
		UPDATE drive SET
			first_seen = MIN(first_seen, (SELECT first_seen FROM drive WHERE drive_id = ?2)),
			last_seen = MAX(last_seen, (SELECT last_seen FROM drive WHERE drive_id = ?2)),
			wwn = CASE WHEN wwn = '' THEN (SELECT wwn FROM drive WHERE drive_id = ?2) ELSE wwn END,
			serial = CASE WHEN serial = '' THEN (SELECT serial FROM drive WHERE drive_id = ?2) ELSE serial END,
			model = CASE WHEN model = '' THEN (SELECT model FROM drive WHERE drive_id = ?2) ELSE model END,
			vendor = CASE WHEN vendor = '' THEN (SELECT vendor FROM drive WHERE drive_id = ?2) ELSE vendor END
		WHERE drive_id = ?1`, into, from); err != nil {
		return Event{}, err
	}
	if _, err := t.ExecContext(ctx, `UPDATE drive SET merged_into = ? WHERE drive_id = ?`, into, from); err != nil {
		return Event{}, err
	}
	if err := t.event(EventMerged, into, 0, map[string]any{"from_drive_id": from, "from_serial": fromSerial, "from_wwn": fromWWN, "from_model": fromModel}, "user:"+actor, 0); err != nil {
		return Event{}, err
	}
	if err := sqlTx.Commit(); err != nil {
		return Event{}, err
	}
	evs, err := s.events(ctx, `WHERE e.drive_id = ? ORDER BY e.event_id DESC LIMIT 1`, into)
	if err != nil || len(evs) == 0 {
		return Event{}, errors.Join(err, ErrNotFound)
	}
	return evs[0], nil
}

// closeDuplicateOpenPlacements keeps the most recently confirmed open
// placement of a drive and closes the rest as merged.
func (t *tx) closeDuplicateOpenPlacements(driveID int64) error {
	_, err := t.ExecContext(t.ctx, `
		UPDATE placement SET ended_at = ?1, end_reason = ?2 WHERE drive_id = ?3 AND ended_at IS NULL AND placement_id != (
			SELECT placement_id FROM placement WHERE drive_id = ?3 AND ended_at IS NULL ORDER BY last_seen DESC, placement_id DESC LIMIT 1)`,
		t.now, EndMerged, driveID)
	return err
}

// MergeHosts folds host from into host into: placements, snapshots, ghosts,
// events and samples move, the newer host's current snapshot wins, and
// from stays as a merged row so its machine id keeps resolving to into.
// Use it when a host was reinstalled and came back with a new machine id.
func (s *Store) MergeHosts(ctx context.Context, intoRef, fromRef, actor string) (Event, error) {
	intoHost, err := s.ResolveHost(ctx, intoRef)
	if err != nil {
		return Event{}, err
	}
	fromHost, err := s.ResolveHost(ctx, fromRef)
	if err != nil {
		return Event{}, err
	}
	into, from := intoHost.ID, fromHost.ID
	if into == from {
		return Event{}, errors.New("both references resolve to the same host")
	}
	now := s.now().Unix()
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, err
	}
	defer sqlTx.Rollback()
	t := &tx{Tx: sqlTx, ctx: ctx, now: now, obs: now}
	for _, table := range []string{"placement", "snapshot", "ghost", "event", "kmsg_sample", "smart_sample", "io_sample", "io_daily"} {
		if _, err := t.ExecContext(ctx, `UPDATE `+table+` SET host_id = ? WHERE host_id = ?`, into, from); err != nil {
			return Event{}, err
		}
	}
	// The host that reported more recently carries the current state.
	if _, err := t.ExecContext(ctx, `
		UPDATE host SET
			current_snapshot_id = (SELECT current_snapshot_id FROM host WHERE host_id = ?2),
			last_observed = (SELECT last_observed FROM host WHERE host_id = ?2),
			last_report = (SELECT last_report FROM host WHERE host_id = ?2),
			degraded = (SELECT degraded FROM host WHERE host_id = ?2),
			stale_since = (SELECT stale_since FROM host WHERE host_id = ?2)
		WHERE host_id = ?1 AND COALESCE((SELECT last_observed FROM host WHERE host_id = ?2), 0) > COALESCE(last_observed, 0)`, into, from); err != nil {
		return Event{}, err
	}
	if _, err := t.ExecContext(ctx, `UPDATE host SET first_seen = MIN(first_seen, (SELECT first_seen FROM host WHERE host_id = ?2)), merged_into = NULL WHERE host_id = ?1`, into, from); err != nil {
		return Event{}, err
	}
	if _, err := t.ExecContext(ctx, `UPDATE host SET merged_into = ?, current_snapshot_id = NULL WHERE host_id = ?`, into, from); err != nil {
		return Event{}, err
	}
	// A drive open on both rows is open twice on one now.
	rows, err := t.QueryContext(ctx, `SELECT drive_id FROM placement WHERE host_id = ? AND ended_at IS NULL GROUP BY drive_id HAVING COUNT(*) > 1`, into)
	if err != nil {
		return Event{}, err
	}
	var dups []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		dups = append(dups, id)
	}
	rows.Close()
	for _, id := range dups {
		if err := t.closeDuplicateOpenPlacements(id); err != nil {
			return Event{}, err
		}
	}
	if err := t.event(EventHostMerged, 0, into, map[string]any{"from_host_id": from, "from_hostname": fromHost.Hostname, "from_machine_id": fromHost.MachineID}, "user:"+actor, 0); err != nil {
		return Event{}, err
	}
	if err := sqlTx.Commit(); err != nil {
		return Event{}, err
	}
	evs, err := s.events(ctx, `WHERE e.host_id = ? AND e.kind = ? ORDER BY e.event_id DESC LIMIT 1`, into, EventHostMerged)
	if err != nil || len(evs) == 0 {
		return Event{}, errors.Join(err, fmt.Errorf("host merged event: %w", ErrNotFound))
	}
	return evs[0], nil
}
