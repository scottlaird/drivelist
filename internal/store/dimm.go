package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// dimmRow is a dimm row.
type dimmRow struct {
	DIMM
	firstSeen, lastSeen int64
	gone                sql.NullInt64
}

const dimmColumns = `d.key, d.slot, d.bank, d.size_bytes, d.ranks, d.type, d.speed_mts, d.manufacturer, d.part, d.serial, d.edac, d.edac_type, d.edac_size_bytes, d.mapping, d.ce, d.ue, d.first_seen, d.last_seen, d.gone_at`

func scanDIMM(rows interface{ Scan(...any) error }) (dimmRow, error) {
	var d dimmRow
	var key string
	err := rows.Scan(&key, &d.Slot, &d.Bank, &d.SizeBytes, &d.Ranks, &d.Type, &d.SpeedMTs, &d.Manufacturer, &d.Part, &d.Serial, &d.EDAC, &d.EDACType, &d.EDACBytes, &d.Mapping, &d.CE, &d.UE, &d.firstSeen, &d.lastSeen, &d.gone)
	return d, err
}

func (t *tx) dimms(hostID int64) (map[string]dimmRow, error) {
	rows, err := t.QueryContext(t.ctx, `SELECT `+dimmColumns+` FROM dimm d WHERE d.host_id = ?`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]dimmRow{}
	for rows.Next() {
		d, err := scanDIMM(rows)
		if err != nil {
			return nil, err
		}
		out[d.Key()] = d
	}
	return out, rows.Err()
}

// ingestDIMMs folds the report's memory modules into the host's dimm
// rows: a module appearing, vanishing, or changing serial in its slot is
// a dimm_changed event; EDAC counts that grew since the last report
// leave a sample and, once per module per day, a memory_errors event.
// The counts count since boot, so the first report from a new boot
// (by boot id) restarts them. A report with no modules at all (an older
// agent, a host that describes none) changes nothing.
func (t *tx) ingestDIMMs(host *hostRow, r Report) error {
	if len(r.DIMMs) == 0 {
		return nil
	}
	fresh := false
	if r.Host.BootID != "" {
		var boot string
		if err := t.QueryRowContext(t.ctx, `SELECT dimm_boot_id FROM host WHERE host_id = ?`, host.id).Scan(&boot); err != nil {
			return err
		}
		if boot != r.Host.BootID {
			fresh = boot != ""
			if _, err := t.ExecContext(t.ctx, `UPDATE host SET dimm_boot_id = ? WHERE host_id = ?`, r.Host.BootID, host.id); err != nil {
				return err
			}
		}
	}
	if r.MemTotalBytes > 0 {
		if _, err := t.ExecContext(t.ctx, `UPDATE host SET mem_total_bytes = ? WHERE host_id = ?`, r.MemTotalBytes, host.id); err != nil {
			return err
		}
	}
	prev, err := t.dimms(host.id)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, d := range r.DIMMs {
		key := d.Key()
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		p, ok := prev[key]
		detail := map[string]any{"slot": d.Slot, "edac": d.EDAC, "serial": d.Serial, "part": d.Part, "size_bytes": d.SizeBytes}
		first := t.obs
		switch {
		case !ok || p.gone.Valid:
			detail["change"] = "appeared"
			if err := t.event(EventDimmChanged, 0, host.id, detail, "report", 0); err != nil {
				return err
			}
		case p.Serial != d.Serial && p.Serial != "" && d.Serial != "":
			// A new module in the slot: its own first sight, its own counts.
			detail["change"], detail["from_serial"], detail["from_part"] = "replaced", p.Serial, p.Part
			if err := t.event(EventDimmChanged, 0, host.id, detail, "report", 0); err != nil {
				return err
			}
		default:
			first = p.firstSeen
			// Counts since boot, compared within one boot only. A
			// replaced module's counts are its own.
			if !fresh && d.CE >= p.CE && d.UE >= p.UE && (d.CE > p.CE || d.UE > p.UE) {
				grewCE, grewUE := d.CE-p.CE, d.UE-p.UE
				if _, err := t.ExecContext(t.ctx, `INSERT OR REPLACE INTO dimm_sample (host_id, key, ts, ce, ue) VALUES (?, ?, ?, ?, ?)`, host.id, key, t.obs, grewCE, grewUE); err != nil {
					return err
				}
				if err := t.memoryErrorsWarning(host.id, key, d, grewCE, grewUE); err != nil {
					return err
				}
			}
		}
		if _, err := t.ExecContext(t.ctx, `
			INSERT INTO dimm (host_id, key, slot, bank, size_bytes, ranks, type, speed_mts, manufacturer, part, serial, edac, edac_type, edac_size_bytes, mapping, ce, ue, first_seen, last_seen, gone_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
			ON CONFLICT (host_id, key) DO UPDATE SET slot = excluded.slot, bank = excluded.bank, size_bytes = excluded.size_bytes, ranks = excluded.ranks, type = excluded.type,
				speed_mts = excluded.speed_mts, manufacturer = excluded.manufacturer, part = excluded.part, serial = excluded.serial, edac = excluded.edac, edac_type = excluded.edac_type,
				edac_size_bytes = excluded.edac_size_bytes, mapping = excluded.mapping, ce = excluded.ce, ue = excluded.ue, first_seen = excluded.first_seen, last_seen = excluded.last_seen, gone_at = NULL`,
			host.id, key, d.Slot, d.Bank, d.SizeBytes, d.Ranks, d.Type, d.SpeedMTs, d.Manufacturer, d.Part, d.Serial, d.EDAC, d.EDACType, d.EDACBytes, d.Mapping, d.CE, d.UE, first, t.obs); err != nil {
			return err
		}
	}
	for key, p := range prev {
		if seen[key] || p.gone.Valid {
			continue
		}
		if _, err := t.ExecContext(t.ctx, `UPDATE dimm SET gone_at = ? WHERE host_id = ? AND key = ?`, t.obs, host.id, key); err != nil {
			return err
		}
		detail := map[string]any{"change": "vanished", "slot": p.Slot, "edac": p.EDAC, "serial": p.Serial, "part": p.Part, "size_bytes": p.SizeBytes, "last_seen": p.lastSeen}
		if err := t.event(EventDimmChanged, 0, host.id, detail, "report", 0); err != nil {
			return err
		}
	}
	return nil
}

// memoryErrorsWarning records one memory_errors event per module and UTC
// day, with the growth that triggered it and the totals since boot.
func (t *tx) memoryErrorsWarning(hostID int64, key string, d DIMM, grewCE, grewUE uint64) error {
	day := time.Unix(t.obs, 0).UTC().Truncate(24 * time.Hour)
	var n int
	if err := t.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM event WHERE kind = ? AND host_id = ? AND ts >= ? AND ts < ? AND detail LIKE ?`,
		EventMemoryErrors, hostID, day.Unix(), day.Add(24*time.Hour).Unix(), fmt.Sprintf(`%%"key":"%s"%%`, key)).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	detail := map[string]any{"key": key, "label": dimmLabel(d), "slot": d.Slot, "edac": d.EDAC, "serial": d.Serial, "part": d.Part, "mapping": d.Mapping,
		"grew": map[string]uint64{"ce": grewCE, "ue": grewUE}, "total": map[string]uint64{"ce": d.CE, "ue": d.UE}}
	return t.event(EventMemoryErrors, 0, hostID, detail, "report", 0)
}

// dimmForEDAC finds the present module whose EDAC location list contains
// loc ("mc0/csrow3/ch1"), for naming a hardware_error event.
func (t *tx) dimmForEDAC(hostID int64, loc string) (DIMM, bool) {
	if loc == "" {
		return DIMM{}, false
	}
	row := t.QueryRowContext(t.ctx, `SELECT `+dimmColumns+` FROM dimm d WHERE d.host_id = ? AND d.gone_at IS NULL AND ('+' || d.edac || '+') LIKE ? LIMIT 1`, hostID, "%+"+loc+"+%")
	d, err := scanDIMM(row)
	if err != nil {
		return DIMM{}, false
	}
	return d.DIMM, true
}

// DIMMRow is one module on one host with what the server has counted.
type DIMMRow struct {
	Hostname  string
	DIMM      DIMM
	FirstSeen time.Time
	LastSeen  time.Time
	CEDay     uint64 // growth counted in the last 24 h
	UEDay     uint64
	LastError time.Time // when a count last grew; zero if never
}

// Problem reports whether the module needs attention: any uncorrected
// error since boot, or corrected errors in the last day.
func (r DIMMRow) Problem() bool { return r.DIMM.UE > 0 || r.UEDay > 0 || r.CEDay > 0 }

// ListDIMMs returns every present module, problems first, then by host
// and slot. host narrows to one host; problems keeps only the ones
// Problem() is true for.
func (s *Store) ListDIMMs(ctx context.Context, host string, problems bool) ([]DIMMRow, error) {
	where, args := `WHERE d.gone_at IS NULL AND h.merged_into IS NULL`, []any{}
	if host != "" {
		h, err := s.ResolveHost(ctx, host)
		if err != nil {
			return nil, err
		}
		where += ` AND d.host_id = ?`
		args = append(args, h.ID)
	}
	dayAgo := s.now().Add(-24 * time.Hour).Unix()
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.hostname, `+dimmColumns+`,
		  COALESCE((SELECT SUM(ce) FROM dimm_sample x WHERE x.host_id = d.host_id AND x.key = d.key AND x.ts >= MAX(`+fmt.Sprint(dayAgo)+`, d.first_seen)), 0),
		  COALESCE((SELECT SUM(ue) FROM dimm_sample x WHERE x.host_id = d.host_id AND x.key = d.key AND x.ts >= MAX(`+fmt.Sprint(dayAgo)+`, d.first_seen)), 0),
		  (SELECT MAX(ts) FROM dimm_sample x WHERE x.host_id = d.host_id AND x.key = d.key AND x.ts >= d.first_seen)
		FROM dimm d JOIN host h USING (host_id) `+where+` ORDER BY h.hostname, d.slot, d.edac`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DIMMRow
	for rows.Next() {
		var r DIMMRow
		var d dimmRow
		var key string
		var last sql.NullInt64
		if err := rows.Scan(&r.Hostname, &key, &d.Slot, &d.Bank, &d.SizeBytes, &d.Ranks, &d.Type, &d.SpeedMTs, &d.Manufacturer, &d.Part, &d.Serial, &d.EDAC, &d.EDACType, &d.EDACBytes, &d.Mapping, &d.CE, &d.UE, &d.firstSeen, &d.lastSeen, &d.gone,
			&r.CEDay, &r.UEDay, &last); err != nil {
			return nil, err
		}
		r.DIMM, r.FirstSeen, r.LastSeen, r.LastError = d.DIMM, time.Unix(d.firstSeen, 0).UTC(), time.Unix(d.lastSeen, 0).UTC(), unix(last)
		if problems && !r.Problem() {
			continue
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := out[i].Problem(), out[j].Problem()
		if pi != pj {
			return pi
		}
		return false
	})
	return out, nil
}

// MemorySummary is one host's memory three ways; see the proto.
type MemorySummary struct {
	Hostname                                         string
	KernelBytes, FirmwareBytes, EDACBytes, Unmatched uint64
	Modules                                          int
	Note                                             string
}

// MemorySummaries checks each host's module list against the kernel's
// total: the EDAC total should equal the firmware's, and the kernel's
// should be a little under it (reserved memory), but not far under and
// never over. host narrows to one.
func (s *Store) MemorySummaries(ctx context.Context, host string) ([]MemorySummary, error) {
	where, args := `WHERE h.merged_into IS NULL`, []any{}
	if host != "" {
		h, err := s.ResolveHost(ctx, host)
		if err != nil {
			return nil, err
		}
		where += ` AND h.host_id = ?`
		args = append(args, h.ID)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.hostname, h.mem_total_bytes,
		  COALESCE(SUM(CASE WHEN d.slot != '' THEN d.size_bytes END), 0),
		  COALESCE(SUM(CASE WHEN d.slot != '' THEN d.edac_size_bytes END), 0),
		  COALESCE(SUM(CASE WHEN d.slot = '' THEN d.edac_size_bytes END), 0),
		  COUNT(CASE WHEN d.slot != '' THEN 1 END)
		FROM host h JOIN dimm d ON d.host_id = h.host_id AND d.gone_at IS NULL `+where+` GROUP BY h.host_id ORDER BY h.hostname`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemorySummary
	for rows.Next() {
		var m MemorySummary
		if err := rows.Scan(&m.Hostname, &m.KernelBytes, &m.FirmwareBytes, &m.EDACBytes, &m.Unmatched, &m.Modules); err != nil {
			return nil, err
		}
		m.Note = memoryNote(m)
		out = append(out, m)
	}
	return out, rows.Err()
}

// memoryNote says what disagrees, or nothing.
func memoryNote(m MemorySummary) string {
	var notes []string
	if m.EDACBytes > 0 && m.EDACBytes != m.FirmwareBytes {
		notes = append(notes, "EDAC's total differs from the firmware's: some module is matched to the wrong entries")
	}
	if m.Unmatched > 0 {
		notes = append(notes, "EDAC lists memory no slot was matched to")
	}
	if m.KernelBytes > 0 && m.FirmwareBytes > 0 {
		switch {
		case m.KernelBytes > m.FirmwareBytes:
			notes = append(notes, "the kernel sees more than the firmware lists: a module is missing from the list")
		case m.KernelBytes*100 < m.FirmwareBytes*90:
			notes = append(notes, "the kernel sees less than 90% of what the firmware lists: a module may be disabled or mapped out")
		}
	}
	return strings.Join(notes, "; ")
}

// dimmLabel is a short name for a module in messages: its slot, else
// its EDAC location.
func dimmLabel(d DIMM) string {
	if d.Slot != "" {
		return d.Slot
	}
	return strings.SplitN(d.EDAC, "+", 2)[0]
}
