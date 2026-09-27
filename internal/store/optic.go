package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/scottlaird/drivelist/collect"
)

// Optic is one pluggable module as an agent reports it, and as the
// listings return it with its latest reading. See collect.Optic.
type Optic struct {
	Port        string
	Ports       []string
	Form        string
	Identifier  string
	Kind        string
	Vendor      string
	OUI         string
	Part        string
	Rev         string
	Serial      string
	DateCode    string
	Compliance  string
	Connector   string
	Wavelength  float64
	Link        string
	Diagnostics bool
	TempC       *float64
	VoltageV    *float64
	Lanes       []OpticLane
	Thresholds  map[string]float64
	Flags       []string
}

// OpticLane is one lane's measurements.
type OpticLane struct {
	Lane   int      `json:"lane"`
	BiasMA *float64 `json:"bias_ma,omitempty"`
	TxMW   *float64 `json:"tx_mw,omitempty"`
	RxMW   *float64 `json:"rx_mw,omitempty"`
}

// Key is what an optic is tracked by: vendor, part and serial; "" for a
// module with no serial.
func (o Optic) Key() string {
	if o.Serial == "" {
		return ""
	}
	return strings.Join([]string{o.Vendor, o.Part, o.Serial}, "|")
}

// OpticRow is one optic, where it is (or was last), and its latest
// reading.
type OpticRow struct {
	ID        int64
	Optic     Optic
	Status    string
	Hostname  string
	Present   bool
	FirstSeen time.Time
	LastSeen  time.Time
	SampledAt time.Time
	Problems  []string
	Dark      bool
}

// OpticPlacement is one stay in one port.
type OpticPlacement struct {
	Hostname  string
	Port      string
	Ports     []string
	FirstSeen time.Time
	LastSeen  time.Time
	EndedAt   time.Time // zero while it is there
	EndReason string
}

// OpticSample is the last reading of one hour.
type OpticSample struct {
	TS       time.Time
	Hostname string
	Port     string
	TempC    *float64
	VoltageV *float64
	Lanes    []OpticLane
	Flags    []string
	Link     string
}

// opticDark is whether low light is expected: the port's link is known
// and not up, so the far end is off, the fibre is unplugged, or the port
// is shut and its laser off.
func opticDark(link string) bool { return link != "" && link != "up" }

// darkFlag is a low-light flag on a dark port: rx, tx or bias low, which
// says nothing about the module.
func darkFlag(flag string) bool {
	f := strings.Fields(flag)
	return len(f) >= 2 && f[1] == "low" && (f[0] == "rx" || f[0] == "tx" || f[0] == "bias")
}

// OpticProblems is what is wrong with an optic by its latest reading
// and its status: the flags the module raised, and any measurement past
// one of the module's own thresholds that it did not flag (not every
// module implements the flags), named the same way ("rx low warning
// lane 3"). On a dark port, low light is expected and left out; dark
// reports that. A module the listing is not showing a current reading
// for gets only its status.
func OpticProblems(o Optic, status string, current bool) (problems []string, dark bool) {
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			problems = append(problems, p)
		}
	}
	if status == StatusSuspect || status == StatusBad {
		add("marked " + status)
	}
	if !current {
		return problems, false
	}
	dark = opticDark(o.Link)
	for _, f := range o.Flags {
		if dark && darkFlag(f) {
			continue
		}
		add(f)
	}
	check := func(q string, v *float64, lane string) {
		if v == nil {
			return
		}
		for _, lvl := range []string{"high_alarm", "low_alarm", "high_warning", "low_warning"} {
			limit, ok := o.Thresholds[q+"_"+lvl]
			if !ok || (strings.HasPrefix(lvl, "high") && limit == 0) {
				continue
			}
			past := *v > limit
			if strings.HasPrefix(lvl, "low") {
				past = *v < limit
			}
			if !past {
				continue
			}
			name := q + " " + strings.ReplaceAll(lvl, "_", " ") + lane
			if dark && darkFlag(name) {
				return
			}
			// An alarm covers the warning on the same side.
			if strings.HasSuffix(lvl, "warning") && seen[q+" "+strings.Replace(strings.ReplaceAll(lvl, "_", " "), "warning", "alarm", 1)+lane] {
				return
			}
			add(name)
			return
		}
	}
	check("temp", o.TempC, "")
	check("voltage", o.VoltageV, "")
	for _, l := range o.Lanes {
		lane := ""
		if len(o.Lanes) > 1 {
			lane = " lane " + strconv.Itoa(l.Lane)
		}
		check("bias", l.BiasMA, lane)
		check("tx", l.TxMW, lane)
		check("rx", l.RxMW, lane)
	}
	return problems, dark
}

func jsonText(v any, empty string) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return empty
	}
	return string(b)
}

// ingestOptics folds the report's optics into the fleet: an optic seen
// for the first time, back in a port, moved to another port or host, or
// gone from its port is an event; each report leaves the hour's latest
// reading; a flag the module newly raises is an optic_alarm event, once
// per optic, flag and UTC day (low-light flags on a dark port are not).
// A report whose agent did not look for optics changes nothing.
func (t *tx) ingestOptics(host *hostRow, r Report) error {
	if !r.OpticsCollected {
		return nil
	}
	type openRow struct {
		id   int64
		port string
	}
	open := map[int64]openRow{}
	rows, err := t.QueryContext(t.ctx, `SELECT placement_id, optic_id, port FROM optic_placement WHERE host_id = ? AND ended_at IS NULL`, host.id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p openRow
		var opticID int64
		if err := rows.Scan(&p.id, &opticID, &p.port); err != nil {
			rows.Close()
			return err
		}
		open[opticID] = p
	}
	rows.Close()

	hour := time.Unix(t.obs, 0).UTC().Truncate(time.Hour).Unix()
	seen := map[int64]bool{}
	for _, o := range r.Optics {
		key := o.Key()
		if key == "" {
			key = fmt.Sprintf("port:%d:%s", host.id, o.Port)
		}
		id, prevFlags, created, err := t.upsertOptic(key, o)
		if err != nil {
			return err
		}
		if seen[id] {
			continue // the same module twice in one report: keep the first
		}
		seen[id] = true
		ident := map[string]any{"serial": o.Serial, "vendor": o.Vendor, "part": o.Part, "form": o.Form, "port": o.Port}
		if created {
			if err := t.opticEvent(EventOpticFirstSeen, id, host.id, ident); err != nil {
				return err
			}
		}
		ports := strings.Join(o.Ports, " ")
		if p, ok := open[id]; ok && p.port == o.Port {
			if _, err := t.ExecContext(t.ctx, `UPDATE optic_placement SET last_seen = ?, ports = ?, link = ? WHERE placement_id = ?`, t.obs, ports, o.Link, p.id); err != nil {
				return err
			}
		} else {
			if ok {
				// Another port on this host.
				if err := t.endOpticPlacement(p.id, "port"); err != nil {
					return err
				}
				d := clone(ident)
				d["from_port"] = p.port
				if err := t.opticEvent(EventOpticMoved, id, host.id, d); err != nil {
					return err
				}
			} else {
				// Another host, if it is still open there.
				var otherID int64
				var otherPort, otherHost string
				err := t.QueryRowContext(t.ctx, `SELECT p.placement_id, p.port, h.hostname FROM optic_placement p JOIN host h USING (host_id) WHERE p.optic_id = ? AND p.ended_at IS NULL`, id).Scan(&otherID, &otherPort, &otherHost)
				switch {
				case err == nil:
					if err := t.endOpticPlacement(otherID, "moved"); err != nil {
						return err
					}
					d := clone(ident)
					d["from_host"], d["from_port"] = otherHost, otherPort
					if err := t.opticEvent(EventOpticMoved, id, host.id, d); err != nil {
						return err
					}
				case errors.Is(err, sql.ErrNoRows):
					if !created {
						if err := t.opticEvent(EventOpticAppeared, id, host.id, ident); err != nil {
							return err
						}
					}
				default:
					return err
				}
			}
			if _, err := t.ExecContext(t.ctx, `INSERT INTO optic_placement (optic_id, host_id, port, ports, link, first_seen, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				id, host.id, o.Port, ports, o.Link, t.obs, t.obs); err != nil {
				return err
			}
		}

		if _, err := t.ExecContext(t.ctx, `INSERT OR REPLACE INTO optic_sample (optic_id, host_id, hour, ts, port, temp_c, voltage_v, lanes, flags, link) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, host.id, hour, t.obs, o.Port, o.TempC, o.VoltageV, jsonText(o.Lanes, "[]"), jsonText(o.Flags, "[]"), o.Link); err != nil {
			return err
		}

		was := map[string]bool{}
		for _, f := range prevFlags {
			was[f] = true
		}
		for _, f := range o.Flags {
			if was[f] || (opticDark(o.Link) && darkFlag(f)) {
				continue
			}
			if err := t.opticAlarm(id, host.id, ident, f); err != nil {
				return err
			}
		}
	}
	for id, p := range open {
		if seen[id] {
			continue
		}
		if err := t.endOpticPlacement(p.id, "vanished"); err != nil {
			return err
		}
		var serial, vendor, part, form string
		if err := t.QueryRowContext(t.ctx, `SELECT serial, vendor, part, form FROM optic WHERE optic_id = ?`, id).Scan(&serial, &vendor, &part, &form); err != nil {
			return err
		}
		if _, err := t.ExecContext(t.ctx, `UPDATE optic SET flags = '[]' WHERE optic_id = ?`, id); err != nil {
			return err
		}
		if err := t.opticEvent(EventOpticVanished, id, host.id, map[string]any{"serial": serial, "vendor": vendor, "part": part, "form": form, "port": p.port}); err != nil {
			return err
		}
	}
	return nil
}

// upsertOptic finds or creates the optic for key, refreshing what the
// module says of itself, and returns the flags it had raised before.
func (t *tx) upsertOptic(key string, o Optic) (id int64, prevFlags []string, created bool, err error) {
	var flags string
	err = t.QueryRowContext(t.ctx, `SELECT optic_id, flags FROM optic WHERE key = ?`, key).Scan(&id, &flags)
	thresholds := jsonText(o.Thresholds, "{}")
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := t.ExecContext(t.ctx, `
			INSERT INTO optic (key, form, identifier, kind, vendor, oui, part, rev, serial, date_code, compliance, connector, wavelength_nm, diagnostics, thresholds, flags, first_seen, last_seen)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			key, o.Form, o.Identifier, o.Kind, o.Vendor, o.OUI, o.Part, o.Rev, o.Serial, o.DateCode, o.Compliance, o.Connector, o.Wavelength, o.Diagnostics, thresholds, jsonText(o.Flags, "[]"), t.obs, t.obs)
		if err != nil {
			return 0, nil, false, err
		}
		id, _ = res.LastInsertId()
		return id, nil, true, nil
	case err != nil:
		return 0, nil, false, err
	}
	_ = json.Unmarshal([]byte(flags), &prevFlags)
	_, err = t.ExecContext(t.ctx, `
		UPDATE optic SET form = ?, identifier = ?, kind = ?, oui = ?, rev = ?, date_code = ?, compliance = ?, connector = ?, wavelength_nm = ?, diagnostics = ?,
			thresholds = CASE WHEN ? = '{}' THEN thresholds ELSE ? END, flags = ?, last_seen = ? WHERE optic_id = ?`,
		o.Form, o.Identifier, o.Kind, o.OUI, o.Rev, o.DateCode, o.Compliance, o.Connector, o.Wavelength, o.Diagnostics, thresholds, thresholds, jsonText(o.Flags, "[]"), t.obs, id)
	return id, prevFlags, false, err
}

func (t *tx) endOpticPlacement(id int64, reason string) error {
	_, err := t.ExecContext(t.ctx, `UPDATE optic_placement SET ended_at = ?, end_reason = ? WHERE placement_id = ?`, t.obs, reason, id)
	return err
}

// opticEvent appends an event about an optic.
func (t *tx) opticEvent(kind string, opticID, hostID int64, detail map[string]any) error {
	return t.opticEventBy(kind, opticID, hostID, detail, "report")
}

func (t *tx) opticEventBy(kind string, opticID, hostID int64, detail map[string]any, source string) error {
	body, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = t.ExecContext(t.ctx, `INSERT INTO event (ts, kind, host_id, optic_id, detail, source) VALUES (?, ?, ?, ?, ?, ?)`,
		t.obs, kind, nullID(hostID), opticID, string(body), source)
	return err
}

// opticAlarm records one optic_alarm per optic, flag and UTC day.
func (t *tx) opticAlarm(opticID, hostID int64, ident map[string]any, flag string) error {
	day := time.Unix(t.obs, 0).UTC().Truncate(24 * time.Hour)
	var n int
	if err := t.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM event WHERE kind = ? AND optic_id = ? AND ts >= ? AND ts < ? AND detail LIKE ?`,
		EventOpticAlarm, opticID, day.Unix(), day.Add(24*time.Hour).Unix(), fmt.Sprintf(`%%"flag":%q%%`, flag)).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	d := clone(ident)
	d["flag"] = flag
	return t.opticEvent(EventOpticAlarm, opticID, hostID, d)
}

const opticRowColumns = `o.optic_id, o.form, o.identifier, o.kind, o.vendor, o.oui, o.part, o.rev, o.serial, o.date_code, o.compliance, o.connector, o.wavelength_nm, o.diagnostics,
	o.thresholds, o.status, o.first_seen, o.last_seen, COALESCE(h.hostname, ''), COALESCE(p.port, ''), COALESCE(p.ports, ''), COALESCE(p.link, ''), p.ended_at IS NULL AND p.placement_id IS NOT NULL`

// opticRows lists optics at their current placement, or at their last one
// with all. where narrows by optic or placement columns.
func (s *Store) opticRows(ctx context.Context, all bool, where string, args ...any) ([]OpticRow, error) {
	cond := `p.ended_at IS NULL`
	if all {
		cond = `1=1`
	}
	if where != "" {
		cond += " AND " + where
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+opticRowColumns+`
		FROM optic o
		LEFT JOIN optic_placement p ON p.placement_id = (SELECT x.placement_id FROM optic_placement x WHERE x.optic_id = o.optic_id ORDER BY x.ended_at IS NULL DESC, x.last_seen DESC, x.placement_id DESC LIMIT 1)
		LEFT JOIN host h ON h.host_id = p.host_id
		WHERE `+cond, args...)
	if err != nil {
		return nil, err
	}
	var out []OpticRow
	for rows.Next() {
		var r OpticRow
		var first, last int64
		var thresholds, ports string
		o := &r.Optic
		if err := rows.Scan(&r.ID, &o.Form, &o.Identifier, &o.Kind, &o.Vendor, &o.OUI, &o.Part, &o.Rev, &o.Serial, &o.DateCode, &o.Compliance, &o.Connector, &o.Wavelength, &o.Diagnostics,
			&thresholds, &r.Status, &first, &last, &r.Hostname, &o.Port, &ports, &o.Link, &r.Present); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(thresholds), &o.Thresholds)
		o.Ports = strings.Fields(ports)
		r.FirstSeen, r.LastSeen = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		sm, err := s.opticSamples(ctx, out[i].ID, time.Time{}, 1)
		if err != nil {
			return nil, err
		}
		if len(sm) == 1 && out[i].Present {
			o := &out[i].Optic
			o.TempC, o.VoltageV, o.Lanes, o.Flags = sm[0].TempC, sm[0].VoltageV, sm[0].Lanes, sm[0].Flags
			if sm[0].Link != "" {
				o.Link = sm[0].Link
			}
			out[i].SampledAt = sm[0].TS
		}
		out[i].Problems, out[i].Dark = OpticProblems(out[i].Optic, out[i].Status, out[i].Present && len(sm) == 1)
	}
	return out, nil
}

// ListOptics returns the optics in ports now (or every optic at its last
// port, with all), problems first, then by host and port in natural
// order. host narrows to one host; problems keeps only optics with one.
func (s *Store) ListOptics(ctx context.Context, host string, problems, all bool) ([]OpticRow, error) {
	where, args := "", []any{}
	if host != "" {
		h, err := s.ResolveHost(ctx, host)
		if err != nil {
			return nil, err
		}
		where, args = "p.host_id = ?", append(args, h.ID)
	}
	rows, err := s.opticRows(ctx, all, where, args...)
	if err != nil {
		return nil, err
	}
	if problems {
		kept := rows[:0]
		for _, r := range rows {
			if len(r.Problems) > 0 {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	sort.SliceStable(rows, func(i, j int) bool {
		pi, pj := len(rows[i].Problems) > 0, len(rows[j].Problems) > 0
		if pi != pj {
			return pi
		}
		if rows[i].Hostname != rows[j].Hostname {
			return rows[i].Hostname < rows[j].Hostname
		}
		return collect.NaturalLess(rows[i].Optic.Port, rows[j].Optic.Port)
	})
	return rows, nil
}

// opticSamples returns an optic's hourly readings since a time, newest
// first, at most limit (0: all).
func (s *Store) opticSamples(ctx context.Context, id int64, since time.Time, limit int) ([]OpticSample, error) {
	q := `SELECT x.ts, COALESCE(h.hostname, ''), x.port, x.temp_c, x.voltage_v, x.lanes, x.flags, x.link FROM optic_sample x LEFT JOIN host h ON h.host_id = x.host_id
		WHERE x.optic_id = ? AND x.hour >= ? ORDER BY x.hour DESC`
	args := []any{id, since.Unix()}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OpticSample
	for rows.Next() {
		var sm OpticSample
		var ts int64
		var temp, volt sql.NullFloat64
		var lanes, flags string
		if err := rows.Scan(&ts, &sm.Hostname, &sm.Port, &temp, &volt, &lanes, &flags, &sm.Link); err != nil {
			return nil, err
		}
		sm.TS = time.Unix(ts, 0).UTC()
		if temp.Valid {
			sm.TempC = &temp.Float64
		}
		if volt.Valid {
			sm.VoltageV = &volt.Float64
		}
		_ = json.Unmarshal([]byte(lanes), &sm.Lanes)
		_ = json.Unmarshal([]byte(flags), &sm.Flags)
		out = append(out, sm)
	}
	return out, rows.Err()
}

// ResolveOptic finds an optic by serial (exact, case-insensitive, or an
// unambiguous prefix), or by HOST:PORT for the module in that port now.
func (s *Store) ResolveOptic(ctx context.Context, ref string) (int64, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, fmt.Errorf("optic %q: %w", ref, ErrNotFound)
	}
	find := func(q string, args ...any) ([]int64, []string, error) {
		rows, err := s.db.QueryContext(ctx, `SELECT optic_id, vendor || ' ' || part || ' ' || serial FROM optic WHERE `+q+` ORDER BY serial`, args...)
		if err != nil {
			return nil, nil, err
		}
		defer rows.Close()
		var ids []int64
		var names []string
		for rows.Next() {
			var id int64
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				return nil, nil, err
			}
			ids, names = append(ids, id), append(names, name)
		}
		return ids, names, rows.Err()
	}
	one := func(ids []int64, names []string) (int64, bool, error) {
		switch len(ids) {
		case 0:
			return 0, false, nil
		case 1:
			return ids[0], true, nil
		}
		return 0, true, &AmbiguousError{Ref: ref, Candidates: names}
	}
	for _, q := range []string{`serial = ? COLLATE NOCASE`, `serial LIKE ? || '%' COLLATE NOCASE`} {
		ids, names, err := find(q, ref)
		if err != nil {
			return 0, err
		}
		if id, ok, err := one(ids, names); ok {
			return id, err
		}
	}
	if hostRef, port, ok := strings.Cut(ref, ":"); ok && port != "" {
		h, err := s.ResolveHost(ctx, hostRef)
		if err != nil {
			return 0, err
		}
		var id int64
		err = s.db.QueryRowContext(ctx, `SELECT optic_id FROM optic_placement WHERE host_id = ? AND ended_at IS NULL AND (port = ? OR ' ' || ports || ' ' LIKE ?)`,
			h.ID, port, "% "+port+" %").Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	return 0, fmt.Errorf("optic %q: %w", ref, ErrNotFound)
}

// OpticDetail is one optic with everything the server keeps about it.
type OpticDetail struct {
	Row        OpticRow
	Placements []OpticPlacement
	Samples    []OpticSample
	Events     []Event
}

// GetOptic returns one optic, its placements, its readings since a time
// (a week when zero) and its events.
func (s *Store) GetOptic(ctx context.Context, ref string, since time.Time) (OpticDetail, error) {
	id, err := s.ResolveOptic(ctx, ref)
	if err != nil {
		return OpticDetail{}, err
	}
	rows, err := s.opticRows(ctx, true, "o.optic_id = ?", id)
	if err != nil {
		return OpticDetail{}, err
	}
	if len(rows) == 0 {
		return OpticDetail{}, fmt.Errorf("optic %q: %w", ref, ErrNotFound)
	}
	d := OpticDetail{Row: rows[0]}
	pr, err := s.db.QueryContext(ctx, `SELECT h.hostname, p.port, p.ports, p.first_seen, p.last_seen, p.ended_at, p.end_reason FROM optic_placement p JOIN host h USING (host_id)
		WHERE p.optic_id = ? ORDER BY p.first_seen DESC, p.placement_id DESC`, id)
	if err != nil {
		return OpticDetail{}, err
	}
	for pr.Next() {
		var p OpticPlacement
		var first, last int64
		var ended sql.NullInt64
		var ports string
		if err := pr.Scan(&p.Hostname, &p.Port, &ports, &first, &last, &ended, &p.EndReason); err != nil {
			pr.Close()
			return OpticDetail{}, err
		}
		p.Ports, p.FirstSeen, p.LastSeen, p.EndedAt = strings.Fields(ports), time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC(), unix(ended)
		d.Placements = append(d.Placements, p)
	}
	pr.Close()
	if since.IsZero() {
		since = s.now().Add(-7 * 24 * time.Hour)
	}
	if d.Samples, err = s.opticSamples(ctx, id, since, 0); err != nil {
		return OpticDetail{}, err
	}
	if d.Events, err = s.events(ctx, `WHERE e.optic_id = ? ORDER BY e.ts DESC, e.event_id DESC LIMIT 200`, id); err != nil {
		return OpticDetail{}, err
	}
	return d, nil
}

// AnnotateOptic records a status change or a note on an optic, as
// Annotate does for drives.
func (s *Store) AnnotateOptic(ctx context.Context, ref, status, note, actor string) (Event, error) {
	if status != "" && !ValidStatus(status) {
		return Event{}, fmt.Errorf("status %q is not one of ok, suspect, bad, shelved, retired", status)
	}
	if status == "" && note == "" {
		return Event{}, errors.New("nothing to record: give a status or a note")
	}
	id, err := s.ResolveOptic(ctx, ref)
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
	var hostID sql.NullInt64
	if err := t.QueryRowContext(ctx, `SELECT host_id FROM optic_placement WHERE optic_id = ? ORDER BY ended_at IS NULL DESC, last_seen DESC LIMIT 1`, id).Scan(&hostID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Event{}, err
	}
	var serial, part, vendor, previous string
	if err := t.QueryRowContext(ctx, `SELECT serial, part, vendor, status FROM optic WHERE optic_id = ?`, id).Scan(&serial, &part, &vendor, &previous); err != nil {
		return Event{}, err
	}
	kind := EventOpticNote
	detail := map[string]any{"note": note, "serial": serial, "part": part, "vendor": vendor}
	if status != "" {
		kind = EventOpticStatusChanged
		detail["status"], detail["previous"] = status, previous
		if _, err := t.ExecContext(ctx, `UPDATE optic SET status = ? WHERE optic_id = ?`, status, id); err != nil {
			return Event{}, err
		}
	}
	if err := t.opticEventBy(kind, id, hostID.Int64, detail, "user:"+actor); err != nil {
		return Event{}, err
	}
	if err := sqlTx.Commit(); err != nil {
		return Event{}, err
	}
	evs, err := s.events(ctx, `WHERE e.optic_id = ? ORDER BY e.event_id DESC LIMIT 1`, id)
	if err != nil || len(evs) == 0 {
		return Event{}, errors.Join(err, ErrNotFound)
	}
	return evs[0], nil
}
