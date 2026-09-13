package store

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// tableRows dumps a table's rows without their ids, for comparing state
// before and after a rebuild.
func (h *harness) tableRows(query string) []map[string]any {
	h.t.Helper()
	rows, err := h.s.db.Query(query)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			h.t.Fatal(err)
		}
		m := map[string]any{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out
}

const placementsQuery = `SELECT drive_id, host_id, enclosure, bay, uses, dev_name, first_seen, last_seen, ended_at, end_reason FROM placement ORDER BY drive_id, first_seen, host_id, bay`
const eventsQuery = `SELECT ts, kind, drive_id, host_id, detail, source FROM event WHERE kind NOT IN ('host_first_seen','host_stale','host_resumed','status_changed','note','identity_conflict','pool_missing_member') ORDER BY ts, drive_id, kind, detail`

// TestRebuildReproducesIngest runs a busy history through Ingest, rebuilds
// from the snapshots, and expects the same placements and derived events.
func TestRebuildReproducesIngest(t *testing.T) {
	h := newHarness(t)
	x := devX
	x.MemberState = "ONLINE"
	h.report(hostA, x, devY, devZ)
	for i := 0; i < 3; i++ {
		h.advance(5 * time.Minute)
		h.report(hostA, x, devY, devZ) // heartbeats extend last_seen
	}
	h.advance(5 * time.Minute)
	h.report(hostA, x, devY) // Z vanishes
	h.advance(time.Hour)
	x.MemberState = "DEGRADED"
	h.report(hostA, x, devY) // state change only
	h.advance(time.Hour)
	h.report(hostB, dev("sdq", "Y1", "0x5000000000000002", "expander-9:0", "7", "spare")) // Y moves, B reports first
	h.advance(time.Minute)
	h.report(hostA, x) // A notices Y gone: no event
	h.advance(time.Hour)
	h.report(hostA, x, devZ) // Z reappears
	h.advance(time.Hour)
	xr, zr := x, devZ
	xr.Expander, zr.Expander = "expander-0:0", "expander-0:0"
	h.report(hostA, xr, zr) // a reboot renumbered the expander: renamed, not moved
	h.advance(time.Hour)
	h.report(hostA, x, devZ) // and back
	h.advance(time.Hour)
	broken := ReportDevice{DevName: "sdc", Expander: "expander-4:0", Bay: "3", Error: "udevadm: exit status 1"}
	h.report(hostA, x, broken) // Z attributed by bay
	h.advance(time.Hour)
	broken.Bay, broken.Expander = "", ""
	h.report(hostA, broken) // degraded: X absent but not vanished
	h.advance(time.Hour)
	z := devZ
	z.Uses = []string{"mount > /backup"}
	h.report(hostA, x, z) // complete again: use change on Z
	h.s.Annotate(h.ctx, "Z1", StatusSuspect, "odd", "me")
	h.advance(30 * time.Minute)
	h.s.Sweep(h.ctx, 5*time.Minute) // B goes stale; its placement stays open

	before := h.tableRows(placementsQuery)
	beforeEvents := h.tableRows(eventsQuery)
	// 3 first_seen, vanished, member_state_changed, moved_host, reappeared,
	// 2 enclosure_renamed, report_degraded, use_changed.
	if len(before) < 6 || len(beforeEvents) < 11 {
		t.Fatalf("history too small to prove anything: %d placements, %d events", len(before), len(beforeEvents))
	}

	res, err := h.s.Rebuild(h.ctx)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if res.Placements != len(before) || res.Events != len(beforeEvents) {
		t.Errorf("Rebuild = %+v, want %d placements and %d events", res, len(before), len(beforeEvents))
	}
	if diff := cmp.Diff(before, h.tableRows(placementsQuery)); diff != "" {
		t.Errorf("placements differ after rebuild (-before +after):\n%s", diff)
	}
	if diff := cmp.Diff(beforeEvents, h.tableRows(eventsQuery)); diff != "" {
		t.Errorf("derived events differ after rebuild (-before +after):\n%s", diff)
	}
	// Kept: the annotation and the stale markers (both hosts had been
	// silent for over three intervals by the sweep).
	if n := h.count(`SELECT COUNT(*) FROM event WHERE kind IN (?, ?)`, EventStatusChanged, EventHostStale); n != 3 {
		t.Errorf("kept events = %d, want 3", n)
	}
	// Ingest keeps working afterwards: a heartbeat is still a heartbeat.
	h.advance(time.Minute)
	if r := h.report(hostA, x, z); r.Changed {
		t.Error("report after rebuild seen as a change")
	}
	// Rebuilding again reproduces the state at that moment (the heartbeat
	// just extended last_seen through the snapshot's last_at).
	mid := h.tableRows(placementsQuery)
	if _, err := h.s.Rebuild(h.ctx); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(mid, h.tableRows(placementsQuery)); diff != "" {
		t.Errorf("second rebuild changed placements:\n%s", diff)
	}
}
