package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// placeholderSerial matches serials firmware fills in when the module
// has none worth the name: all zeros or all Fs (with or without 0x),
// "SerNum0", and the usual not-a-value strings.
var placeholderSerial = regexp.MustCompile(`(?i)^(0x)?(0+|f+)$|^sernum\d*$|^(unknown|not specified|none|n/?a|no dimm|to be filled by o\.e\.m\.)$`)

// ErrNoSerial is the error for a module whose serial is blank or a
// placeholder: there is nothing to key a status on.
var ErrNoSerial = errors.New("no usable serial")

// DIMMIdentifiable reports whether a module's serial can tell it apart
// from others. A status is kept per part and serial, so a module with a
// blank or placeholder serial can't be given one.
func DIMMIdentifiable(serial string) bool {
	s := strings.TrimSpace(serial)
	return s != "" && !placeholderSerial.MatchString(s)
}

// DIMMRef names a memory module across slots and hosts.
type DIMMRef struct {
	Part, Serial string
}

func (r DIMMRef) String() string { return strings.TrimSpace(r.Part + " " + r.Serial) }

// ResolveDIMM finds the module ref names. ref is a serial, an
// unambiguous prefix of one, PART/SERIAL, or HOST:SLOT for the module in
// that slot now (or last). Modules the server has only seen with a
// placeholder serial match only by HOST:SLOT, and then fail: there is
// nothing to key a status on.
func (s *Store) ResolveDIMM(ctx context.Context, ref string) (DIMMRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return DIMMRef{}, fmt.Errorf("dimm %q: %w", ref, ErrNotFound)
	}
	if host, slot, ok := strings.Cut(ref, ":"); ok {
		return s.resolveDIMMSlot(ctx, ref, host, slot)
	}
	find := func(q string, args ...any) ([]DIMMRef, error) {
		rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT part, serial FROM dimm WHERE `+q+` ORDER BY serial, part`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []DIMMRef
		for rows.Next() {
			var r DIMMRef
			if err := rows.Scan(&r.Part, &r.Serial); err != nil {
				return nil, err
			}
			if DIMMIdentifiable(r.Serial) {
				out = append(out, r)
			}
		}
		return out, rows.Err()
	}
	var tries [][]any
	if part, serial, ok := strings.Cut(ref, "/"); ok {
		tries = append(tries, []any{`part = ? COLLATE NOCASE AND serial = ? COLLATE NOCASE`, part, serial})
	}
	tries = append(tries, []any{`serial = ? COLLATE NOCASE`, ref}, []any{`serial LIKE ? || '%' COLLATE NOCASE`, ref})
	for _, try := range tries {
		found, err := find(try[0].(string), try[1:]...)
		if err != nil {
			return DIMMRef{}, err
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return found[0], nil
		}
		var names []string
		for _, f := range found {
			names = append(names, f.Part+"/"+f.Serial)
		}
		return DIMMRef{}, &AmbiguousError{Ref: ref, Candidates: names}
	}
	return DIMMRef{}, fmt.Errorf("dimm %q: %w", ref, ErrNotFound)
}

// resolveDIMMSlot finds the module in a host's slot: the one there now,
// else the last one seen there. The slot matches exactly, ignoring case,
// or as an unambiguous prefix.
func (s *Store) resolveDIMMSlot(ctx context.Context, ref, hostRef, slot string) (DIMMRef, error) {
	h, err := s.ResolveHost(ctx, hostRef)
	if err != nil {
		return DIMMRef{}, err
	}
	slot = strings.TrimSpace(slot)
	type cand struct {
		key, part, serial string
	}
	find := func(q string) ([]cand, error) {
		rows, err := s.db.QueryContext(ctx, `SELECT key, part, serial FROM dimm WHERE host_id = ? AND `+q+` ORDER BY gone_at IS NULL DESC, last_seen DESC`, h.ID, slot)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []cand
		seen := map[string]bool{}
		for rows.Next() {
			var c cand
			if err := rows.Scan(&c.key, &c.part, &c.serial); err != nil {
				return nil, err
			}
			if !seen[c.key] {
				seen[c.key] = true
				out = append(out, c)
			}
		}
		return out, rows.Err()
	}
	for _, q := range []string{`key = ? COLLATE NOCASE`, `key LIKE ? || '%' COLLATE NOCASE`} {
		found, err := find(q)
		if err != nil {
			return DIMMRef{}, err
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			c := found[0]
			if !DIMMIdentifiable(c.serial) {
				return DIMMRef{}, fmt.Errorf("the module in %s %s reports %w (%q), so it can't be told apart from others", h.Hostname, c.key, ErrNoSerial, c.serial)
			}
			return DIMMRef{Part: c.part, Serial: c.serial}, nil
		}
		var names []string
		for _, c := range found {
			names = append(names, h.Hostname+":"+c.key)
		}
		return DIMMRef{}, &AmbiguousError{Ref: ref, Candidates: names}
	}
	return DIMMRef{}, fmt.Errorf("dimm %q: no slot %q on %s: %w", ref, slot, h.Hostname, ErrNotFound)
}

// dimmStatus returns a module's status; ok when nobody has set one.
func dimmStatus(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, r DIMMRef) (string, error) {
	var status string
	err := q.QueryRowContext(ctx, `SELECT status FROM dimm_status WHERE part = ? AND serial = ?`, r.Part, r.Serial).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return StatusOK, nil
	}
	return status, err
}

// AnnotateDIMM records a status change or a note on a memory module, as
// Annotate does for drives. The event goes on the host the module was
// last seen on.
func (s *Store) AnnotateDIMM(ctx context.Context, ref, status, note, actor string) (Event, error) {
	if status != "" && !ValidStatus(status) {
		return Event{}, fmt.Errorf("status %q is not one of ok, suspect, bad, shelved, retired", status)
	}
	if status == "" && note == "" {
		return Event{}, errors.New("nothing to record: give a status or a note")
	}
	m, err := s.ResolveDIMM(ctx, ref)
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
	var hostID int64
	var key string
	if err := t.QueryRowContext(ctx, `SELECT host_id, key FROM dimm WHERE part = ? AND serial = ? ORDER BY gone_at IS NULL DESC, last_seen DESC LIMIT 1`, m.Part, m.Serial).Scan(&hostID, &key); err != nil {
		return Event{}, err
	}
	previous, err := dimmStatus(ctx, t, m)
	if err != nil {
		return Event{}, err
	}
	kind := EventDimmNote
	detail := map[string]any{"note": note, "part": m.Part, "serial": m.Serial, "slot": key}
	if status != "" {
		kind = EventDimmStatusChanged
		detail["status"], detail["previous"] = status, previous
		if _, err := t.ExecContext(ctx, `INSERT INTO dimm_status (part, serial, status) VALUES (?, ?, ?) ON CONFLICT (part, serial) DO UPDATE SET status = excluded.status`, m.Part, m.Serial, status); err != nil {
			return Event{}, err
		}
	}
	if err := t.event(kind, 0, hostID, detail, "user:"+actor, 0); err != nil {
		return Event{}, err
	}
	var id int64
	if err := t.QueryRowContext(ctx, `SELECT MAX(event_id) FROM event WHERE kind = ? AND host_id = ?`, kind, hostID).Scan(&id); err != nil {
		return Event{}, err
	}
	if err := sqlTx.Commit(); err != nil {
		return Event{}, err
	}
	evs, err := s.events(ctx, `WHERE e.event_id = ?`, id)
	if err != nil || len(evs) == 0 {
		return Event{}, errors.Join(err, ErrNotFound)
	}
	return evs[0], nil
}

// DIMMPlace is a slot a module has been seen in.
type DIMMPlace struct {
	Hostname  string
	DIMM      DIMM
	FirstSeen time.Time
	LastSeen  time.Time
	GoneAt    time.Time // zero while it is there
}

// DIMMDetail is one module: its status, every slot it has been seen in
// (newest first), and the events that name it.
type DIMMDetail struct {
	DIMMRef
	Status string
	Places []DIMMPlace
	Events []Event
}

// GetDIMM returns the module ref names; see ResolveDIMM.
func (s *Store) GetDIMM(ctx context.Context, ref string) (DIMMDetail, error) {
	m, err := s.ResolveDIMM(ctx, ref)
	if err != nil {
		return DIMMDetail{}, err
	}
	d := DIMMDetail{DIMMRef: m}
	if d.Status, err = dimmStatus(ctx, s.db, m); err != nil {
		return DIMMDetail{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT h.hostname, `+dimmColumns+` FROM dimm d JOIN host h USING (host_id) WHERE d.part = ? AND d.serial = ? ORDER BY d.gone_at IS NULL DESC, d.last_seen DESC`, m.Part, m.Serial)
	if err != nil {
		return DIMMDetail{}, err
	}
	for rows.Next() {
		var p DIMMPlace
		var r dimmRow
		var key string
		if err := rows.Scan(&p.Hostname, &key, &r.Slot, &r.Bank, &r.SizeBytes, &r.Ranks, &r.Type, &r.SpeedMTs, &r.Manufacturer, &r.Part, &r.Serial, &r.EDAC, &r.EDACType, &r.EDACBytes, &r.Mapping, &r.CE, &r.UE, &r.TotalWidth, &r.DataWidth, &r.EDACMode, &r.firstSeen, &r.lastSeen, &r.gone); err != nil {
			rows.Close()
			return DIMMDetail{}, err
		}
		p.DIMM, p.FirstSeen, p.LastSeen, p.GoneAt = r.DIMM, time.Unix(r.firstSeen, 0).UTC(), time.Unix(r.lastSeen, 0).UTC(), unix(r.gone)
		d.Places = append(d.Places, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return DIMMDetail{}, err
	}
	// Events carry the module's serial in their detail: as "serial" when
	// it is the subject, as "from_serial" when it was replaced.
	quoted, _ := json.Marshal(m.Serial)
	if d.Events, err = s.events(ctx, `WHERE e.kind IN (?, ?, ?, ?, ?) AND (e.detail LIKE ? OR e.detail LIKE ?) ORDER BY e.ts DESC, e.event_id DESC LIMIT 200`,
		EventDimmChanged, EventMemoryErrors, EventHardwareError, EventDimmStatusChanged, EventDimmNote,
		`%"serial":`+string(quoted)+`%`, `%"from_serial":`+string(quoted)+`%`); err != nil {
		return DIMMDetail{}, err
	}
	return d, nil
}
