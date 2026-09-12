package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Host is a host as the query side reports it.
type Host struct {
	ID           int64
	Hostname     string
	MachineID    string
	OS           string
	AgentVersion string
	FirstSeen    time.Time
	LastReport   time.Time
	StaleSince   time.Time // zero while reporting
	DriveCount   int       // open placements
	MissingCount int       // drives last seen here that vanished and are not expected to be absent
	GhostCount   int       // open pool ghosts
}

// Placement is one interval of a drive's history.
type Placement struct {
	Hostname  string
	Expander  string
	Bay       string
	DevName   string
	Uses      []string
	FirstSeen time.Time
	LastSeen  time.Time
	EndedAt   time.Time // zero while current
	EndReason string
}

// Drive is a drive with its current or most recent placement.
type Drive struct {
	ID          int64
	WWN         string
	Vendor      string
	Model       string
	Serial      string
	SizeBytes   uint64
	Bus         string
	Status      string
	FirstSeen   time.Time
	LastSeen    time.Time
	Current     *Placement // nil when not currently placed
	Last        *Placement // the most recent placement when Current is nil
	MemberState string
}

// Event is one ledger entry with its drive and host resolved to names.
type Event struct {
	ID       int64
	TS       time.Time
	Kind     string
	Serial   string
	WWN      string
	Hostname string
	Detail   string // JSON object
	Source   string
}

// Ghost is an open pool member with no present device.
type Ghost struct {
	Hostname  string
	Pool      string
	Path      string
	GUID      string
	State     string
	Serial    string // when the path resolved to a known drive
	WWN       string
	FirstSeen time.Time
	LastSeen  time.Time
}

// DriveFilter narrows ListDrives. Zero means no filter.
type DriveFilter struct {
	Host        string // hostname or unambiguous prefix
	Status      []string
	UnusedOnly  bool
	MissingOnly bool
	Model       string // substring
}

// EventFilter narrows ListEvents.
type EventFilter struct {
	Since time.Time
	Kinds []string
	Host  string
	Limit int // 0: 200
}

// ErrNotFound is returned when a reference matches nothing.
var ErrNotFound = errors.New("not found")

// AmbiguousError is returned when a reference matches more than one thing.
type AmbiguousError struct {
	Ref        string
	Candidates []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%q matches %d: %s", e.Ref, len(e.Candidates), strings.Join(e.Candidates, ", "))
}

// ListHosts returns every host, by name.
func (s *Store) ListHosts(ctx context.Context) ([]Host, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.host_id, h.hostname, h.machine_id, h.os, h.agent_version, h.first_seen, h.last_report, h.stale_since,
		  (SELECT COUNT(*) FROM placement p WHERE p.host_id = h.host_id AND p.ended_at IS NULL),
		  (SELECT COUNT(*) FROM ghost g WHERE g.host_id = h.host_id AND g.ended_at IS NULL)
		FROM host h ORDER BY h.hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []Host
	for rows.Next() {
		var h Host
		var first int64
		var last, stale sql.NullInt64
		if err := rows.Scan(&h.ID, &h.Hostname, &h.MachineID, &h.OS, &h.AgentVersion, &first, &last, &stale, &h.DriveCount, &h.GhostCount); err != nil {
			return nil, err
		}
		h.FirstSeen, h.LastReport, h.StaleSince = time.Unix(first, 0).UTC(), unix(last), unix(stale)
		hosts = append(hosts, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range hosts {
		n, err := s.countMissing(ctx, hosts[i].ID)
		if err != nil {
			return nil, err
		}
		hosts[i].MissingCount = n
	}
	return hosts, nil
}

// missingWhere selects drives that are absent without a status that
// expects it: no open placement, and the most recent placement ended with
// vanished. Bind nothing; it is a fragment over alias d.
const missingWhere = `d.merged_into IS NULL
	AND d.status NOT IN ('bad', 'shelved', 'retired')
	AND NOT EXISTS (SELECT 1 FROM placement p WHERE p.drive_id = d.drive_id AND p.ended_at IS NULL)
	AND (SELECT p.end_reason FROM placement p WHERE p.drive_id = d.drive_id ORDER BY p.first_seen DESC, p.placement_id DESC LIMIT 1) = 'vanished'`

func (s *Store) countMissing(ctx context.Context, hostID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM drive d WHERE `+missingWhere+`
		AND (SELECT p.host_id FROM placement p WHERE p.drive_id = d.drive_id ORDER BY p.first_seen DESC, p.placement_id DESC LIMIT 1) = ?`, hostID).Scan(&n)
	return n, err
}

// ResolveHost finds a host by hostname or unambiguous prefix.
func (s *Store) ResolveHost(ctx context.Context, ref string) (Host, error) {
	hosts, err := s.ListHosts(ctx)
	if err != nil {
		return Host{}, err
	}
	var matches []Host
	for _, h := range hosts {
		if h.Hostname == ref {
			return h, nil
		}
		if strings.HasPrefix(h.Hostname, ref) {
			matches = append(matches, h)
		}
	}
	switch len(matches) {
	case 0:
		return Host{}, fmt.Errorf("host %q: %w", ref, ErrNotFound)
	case 1:
		return matches[0], nil
	}
	names := make([]string, len(matches))
	for i, h := range matches {
		names[i] = h.Hostname
	}
	return Host{}, &AmbiguousError{Ref: ref, Candidates: names}
}

// ResolveDrive finds a drive by serial, WWN, or an unambiguous prefix of
// either. WWNs compare case-insensitively and with or without the 0x.
func (s *Store) ResolveDrive(ctx context.Context, ref string) (int64, error) {
	if ref == "" {
		return 0, fmt.Errorf("drive reference is empty: %w", ErrNotFound)
	}
	lower := strings.ToLower(ref)
	rows, err := s.db.QueryContext(ctx, `
		SELECT drive_id, serial, wwn, model FROM drive WHERE merged_into IS NULL AND (
			serial = ?1 OR lower(wwn) = ?2 OR lower(wwn) = '0x' || ?2
			OR serial LIKE ?1 || '%' OR lower(wwn) LIKE ?2 || '%' OR lower(wwn) LIKE '0x' || ?2 || '%')
		ORDER BY drive_id`, ref, lower)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type cand struct {
		id                 int64
		serial, wwn, model string
	}
	var exact, prefix []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.serial, &c.wwn, &c.model); err != nil {
			return 0, err
		}
		w := strings.ToLower(c.wwn)
		if c.serial == ref || w == lower || w == "0x"+lower {
			exact = append(exact, c)
		} else {
			prefix = append(prefix, c)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	pick := exact
	if len(pick) == 0 {
		pick = prefix
	}
	switch len(pick) {
	case 0:
		return 0, fmt.Errorf("drive %q: %w", ref, ErrNotFound)
	case 1:
		return pick[0].id, nil
	}
	names := make([]string, len(pick))
	for i, c := range pick {
		names[i] = fmt.Sprintf("%s (%s %s)", c.serial, c.model, c.wwn)
	}
	return 0, &AmbiguousError{Ref: ref, Candidates: names}
}

// ListDrives returns drives matching the filter, ordered by host, expander,
// bay, then serial, so a listing reads like the enclosure. Drives with no
// current placement come last.
func (s *Store) ListDrives(ctx context.Context, f DriveFilter) ([]Drive, error) {
	where := []string{"d.merged_into IS NULL"}
	var args []any
	if f.Host != "" {
		h, err := s.ResolveHost(ctx, f.Host)
		if err != nil {
			return nil, err
		}
		where = append(where, "EXISTS (SELECT 1 FROM placement p WHERE p.drive_id = d.drive_id AND p.ended_at IS NULL AND p.host_id = ?)")
		args = append(args, h.ID)
	}
	if len(f.Status) > 0 {
		marks := strings.Repeat("?,", len(f.Status))
		where = append(where, "d.status IN ("+marks[:len(marks)-1]+")")
		for _, st := range f.Status {
			args = append(args, st)
		}
	}
	if f.UnusedOnly {
		where = append(where, "EXISTS (SELECT 1 FROM placement p WHERE p.drive_id = d.drive_id AND p.ended_at IS NULL AND p.uses = '[]')")
	}
	if f.MissingOnly {
		where = append(where, missingWhere)
	}
	if f.Model != "" {
		where = append(where, "d.model LIKE '%' || ? || '%'")
		args = append(args, f.Model)
	}
	return s.drives(ctx, `WHERE `+strings.Join(where, " AND ")+`
		ORDER BY COALESCE((SELECT h.hostname || '/' || p.expander || '/' || printf('%08d', CAST(p.bay AS INTEGER)) || '/' || p.bay FROM placement p JOIN host h USING (host_id) WHERE p.drive_id = d.drive_id AND p.ended_at IS NULL), '~'), d.serial`, args...)
}

// GetDrive returns one drive with every key it has been seen under and the
// event behind its status, if any.
func (s *Store) GetDrive(ctx context.Context, ref string) (Drive, []string, *Event, error) {
	id, err := s.ResolveDrive(ctx, ref)
	if err != nil {
		return Drive{}, nil, nil, err
	}
	ds, err := s.drives(ctx, `WHERE d.drive_id = ?`, id)
	if err != nil {
		return Drive{}, nil, nil, err
	}
	if len(ds) == 0 {
		return Drive{}, nil, nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM drive_key WHERE drive_id = ? ORDER BY key`, id)
	if err != nil {
		return Drive{}, nil, nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return Drive{}, nil, nil, err
		}
		keys = append(keys, k)
	}
	evs, err := s.events(ctx, `WHERE e.drive_id = ? AND e.kind = ? ORDER BY e.ts DESC, e.event_id DESC LIMIT 1`, id, EventStatusChanged)
	if err != nil {
		return Drive{}, nil, nil, err
	}
	var last *Event
	if len(evs) == 1 {
		last = &evs[0]
	}
	return ds[0], keys, last, nil
}

// DriveHistory returns a drive with every placement and event, oldest first.
func (s *Store) DriveHistory(ctx context.Context, ref string) (Drive, []Placement, []Event, error) {
	id, err := s.ResolveDrive(ctx, ref)
	if err != nil {
		return Drive{}, nil, nil, err
	}
	ds, err := s.drives(ctx, `WHERE d.drive_id = ?`, id)
	if err != nil || len(ds) == 0 {
		return Drive{}, nil, nil, errors.Join(err, ErrNotFound)
	}
	ps, err := s.placements(ctx, `WHERE p.drive_id = ? ORDER BY p.first_seen, p.placement_id`, id)
	if err != nil {
		return Drive{}, nil, nil, err
	}
	evs, err := s.events(ctx, `WHERE e.drive_id = ? ORDER BY e.ts, e.event_id`, id)
	if err != nil {
		return Drive{}, nil, nil, err
	}
	return ds[0], ps, evs, nil
}

// ListEvents returns events newest first.
func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]Event, error) {
	where := []string{"1=1"}
	var args []any
	if !f.Since.IsZero() {
		where = append(where, "e.ts >= ?")
		args = append(args, f.Since.Unix())
	}
	if len(f.Kinds) > 0 {
		marks := strings.Repeat("?,", len(f.Kinds))
		where = append(where, "e.kind IN ("+marks[:len(marks)-1]+")")
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if f.Host != "" {
		h, err := s.ResolveHost(ctx, f.Host)
		if err != nil {
			return nil, err
		}
		where = append(where, "e.host_id = ?")
		args = append(args, h.ID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	args = append(args, limit)
	return s.events(ctx, `WHERE `+strings.Join(where, " AND ")+` ORDER BY e.ts DESC, e.event_id DESC LIMIT ?`, args...)
}

// ListMissing returns drives that vanished and are not expected to be
// absent, each with its last placement, and every open ghost.
func (s *Store) ListMissing(ctx context.Context) ([]Drive, []Ghost, error) {
	ds, err := s.drives(ctx, `WHERE `+missingWhere+` ORDER BY d.last_seen DESC`)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.hostname, g.pool, g.member_path, g.member_guid, g.member_state, COALESCE(d.serial, ''), COALESCE(d.wwn, ''), g.first_seen, g.last_seen
		FROM ghost g JOIN host h USING (host_id) LEFT JOIN drive d ON d.drive_id = g.drive_id
		WHERE g.ended_at IS NULL ORDER BY h.hostname, g.pool, g.member_path`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ghosts []Ghost
	for rows.Next() {
		var g Ghost
		var first, last int64
		if err := rows.Scan(&g.Hostname, &g.Pool, &g.Path, &g.GUID, &g.State, &g.Serial, &g.WWN, &first, &last); err != nil {
			return nil, nil, err
		}
		g.FirstSeen, g.LastSeen = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		ghosts = append(ghosts, g)
	}
	return ds, ghosts, rows.Err()
}

// Annotate records a status change (status non-empty) or a note as an
// event from actor, and updates the drive's status.
func (s *Store) Annotate(ctx context.Context, ref, status, note, actor string) (Event, error) {
	if status != "" && !ValidStatus(status) {
		return Event{}, fmt.Errorf("status %q is not one of ok, suspect, bad, shelved, retired", status)
	}
	if status == "" && note == "" {
		return Event{}, errors.New("nothing to record: give a status or a note")
	}
	id, err := s.ResolveDrive(ctx, ref)
	if err != nil {
		return Event{}, err
	}
	now := s.now()
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, err
	}
	defer sqlTx.Rollback()
	t := &tx{Tx: sqlTx, ctx: ctx, now: now.Unix(), obs: now.Unix()}
	kind := EventNote
	detail := map[string]any{"note": note}
	if status != "" {
		kind = EventStatusChanged
		var previous string
		if err := t.QueryRowContext(ctx, `SELECT status FROM drive WHERE drive_id = ?`, id).Scan(&previous); err != nil {
			return Event{}, err
		}
		detail["status"] = status
		detail["previous"] = previous
		if _, err := t.ExecContext(ctx, `UPDATE drive SET status = ? WHERE drive_id = ?`, status, id); err != nil {
			return Event{}, err
		}
	}
	if err := t.event(kind, id, 0, detail, "user:"+actor, 0); err != nil {
		return Event{}, err
	}
	if err := sqlTx.Commit(); err != nil {
		return Event{}, err
	}
	evs, err := s.events(ctx, `WHERE e.drive_id = ? ORDER BY e.event_id DESC LIMIT 1`, id)
	if err != nil || len(evs) == 0 {
		return Event{}, errors.Join(err, ErrNotFound)
	}
	return evs[0], nil
}

// drives loads drives for a WHERE/ORDER BY tail over alias d, with their
// current or last placement.
func (s *Store) drives(ctx context.Context, tail string, args ...any) ([]Drive, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.drive_id, d.wwn, d.vendor, d.model, d.serial, d.size_bytes, d.bus, d.status, d.first_seen, d.last_seen FROM drive d `+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Drive
	for rows.Next() {
		var d Drive
		var first, last int64
		if err := rows.Scan(&d.ID, &d.WWN, &d.Vendor, &d.Model, &d.Serial, &d.SizeBytes, &d.Bus, &d.Status, &first, &last); err != nil {
			return nil, err
		}
		d.FirstSeen, d.LastSeen = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		ps, err := s.placements(ctx, `WHERE p.drive_id = ? ORDER BY p.first_seen DESC, p.placement_id DESC LIMIT 1`, out[i].ID)
		if err != nil {
			return nil, err
		}
		if len(ps) == 1 {
			p := ps[0]
			if p.EndedAt.IsZero() {
				out[i].Current = &p
			} else {
				out[i].Last = &p
			}
		}
		err = s.db.QueryRowContext(ctx, `SELECT sd.member_state FROM snapshot_device sd JOIN host h ON h.current_snapshot_id = sd.snapshot_id WHERE sd.drive_id = ? LIMIT 1`, out[i].ID).Scan(&out[i].MemberState)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) placements(ctx context.Context, tail string, args ...any) ([]Placement, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT h.hostname, p.expander, p.bay, p.dev_name, p.uses, p.first_seen, p.last_seen, p.ended_at, p.end_reason FROM placement p JOIN host h USING (host_id) `+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Placement
	for rows.Next() {
		var p Placement
		var uses string
		var first, last int64
		var ended sql.NullInt64
		if err := rows.Scan(&p.Hostname, &p.Expander, &p.Bay, &p.DevName, &uses, &first, &last, &ended, &p.EndReason); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(uses), &p.Uses)
		p.FirstSeen, p.LastSeen, p.EndedAt = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC(), unix(ended)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) events(ctx context.Context, tail string, args ...any) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.event_id, e.ts, e.kind, COALESCE(d.serial, ''), COALESCE(d.wwn, ''), COALESCE(h.hostname, ''), e.detail, e.source
		FROM event e LEFT JOIN drive d ON d.drive_id = e.drive_id LEFT JOIN host h ON h.host_id = e.host_id `+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.Kind, &e.Serial, &e.WWN, &e.Hostname, &e.Detail, &e.Source); err != nil {
			return nil, err
		}
		e.TS = time.Unix(ts, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
