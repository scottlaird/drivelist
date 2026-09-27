package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }

func sr4(port string, rx3 float64, flags ...string) Optic {
	return Optic{Port: port, Ports: []string{port}, Form: "QSFP28", Kind: "optical", Vendor: "Mellanox", Part: "MMA1B00-C100D", Serial: "MT1918FT01234", Link: "up", Diagnostics: true,
		TempC: f(38), VoltageV: f(3.25),
		Lanes:      []OpticLane{{Lane: 1, RxMW: f(0.75), TxMW: f(0.8)}, {Lane: 2, RxMW: f(0.74), TxMW: f(0.79)}, {Lane: 3, RxMW: f(rx3), TxMW: f(0.81)}, {Lane: 4, RxMW: f(0.76), TxMW: f(0.78)}},
		Thresholds: map[string]float64{"rx_low_warning": 0.1, "rx_low_alarm": 0.05, "temp_high_warning": 70}, Flags: flags}
}

func TestOpticProblems(t *testing.T) {
	o := sr4("swp1", 0.75)
	if p, dark := OpticProblems(o, StatusOK, true); len(p) != 0 || dark {
		t.Errorf("healthy = %v %v", p, dark)
	}
	// Past the warning threshold with no flag raised: named like a flag.
	o = sr4("swp1", 0.08)
	if p, _ := OpticProblems(o, StatusOK, true); len(p) != 1 || p[0] != "rx low warning lane 3" {
		t.Errorf("below warning = %v", p)
	}
	// Past the alarm, and flagged: the alarm, once, and no warning.
	o = sr4("swp1", 0.02, "rx low alarm lane 3")
	if p, _ := OpticProblems(o, StatusOK, true); len(p) != 1 || p[0] != "rx low alarm lane 3" {
		t.Errorf("below alarm = %v", p)
	}
	// A dark port: low light is expected; a hot module is not.
	o = sr4("swp1", 0, "rx low alarm lane 3")
	o.Link, o.TempC = "down", f(72)
	if p, dark := OpticProblems(o, StatusOK, true); !dark || len(p) != 1 || p[0] != "temp high warning" {
		t.Errorf("dark = %v %v", p, dark)
	}
	// Marked, and not current: only the mark.
	if p, _ := OpticProblems(sr4("swp1", 0.02), StatusBad, false); len(p) != 1 || p[0] != "marked bad" {
		t.Errorf("marked = %v", p)
	}
	// A single-lane SFP names no lane, matching its flags.
	sfp := Optic{Link: "up", Lanes: []OpticLane{{Lane: 1, RxMW: f(0.018)}}, Thresholds: map[string]float64{"rx_low_alarm": 0.02}, Flags: []string{"rx low alarm"}}
	if p, _ := OpticProblems(sfp, StatusOK, true); len(p) != 1 || p[0] != "rx low alarm" {
		t.Errorf("SFP = %v", p)
	}
}

// TestIngestOptics walks one optic through the fleet: first seen, an
// hour of readings, a flag it raises, a move to another port, a move to
// another host, vanishing, and coming back; marking and resolving it.
func TestIngestOptics(t *testing.T) {
	h := newHarness(t)
	report := func(host HostIdentity, optics ...Optic) {
		h.submit(Report{Host: host, ObservedAt: h.now, Devices: []ReportDevice{devX}, Complete: true, Optics: optics, OpticsCollected: true})
	}
	kinds := func() []string {
		rows, err := h.s.db.Query(`SELECT kind FROM event WHERE optic_id IS NOT NULL ORDER BY event_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var k string
			rows.Scan(&k)
			out = append(out, k)
		}
		return out
	}
	dac := Optic{Port: "swp10", Ports: []string{"swp10"}, Form: "QSFP28", Kind: "dac", Vendor: "Mellanox", Part: "MCP1600-C001", Serial: "MT1705VS06321", Link: "up"}

	report(hostA, sr4("swp1", 0.75), dac)
	if got := kinds(); len(got) != 2 || got[0] != EventOpticFirstSeen {
		t.Fatalf("events after first sight = %v", got)
	}
	rows, err := h.s.ListOptics(h.ctx, "", false, false)
	if err != nil || len(rows) != 2 || rows[0].Optic.Port != "swp1" || rows[1].Optic.Port != "swp10" || rows[0].Hostname != "storage1" || !rows[0].Present || rows[0].Optic.TempC == nil {
		t.Fatalf("ListOptics = %+v, %v", rows, err)
	}
	if rows[1].Optic.Kind != "dac" || len(rows[1].Optic.Lanes) != 0 || len(rows[1].Problems) != 0 {
		t.Errorf("DAC row = %+v", rows[1])
	}

	// Later in the same hour, then lane 3 dims and the module flags it.
	h.advance(5 * time.Minute)
	report(hostA, sr4("swp1", 0.74), dac)
	h.advance(time.Hour)
	report(hostA, sr4("swp1", 0.04, "rx low alarm lane 3"), dac)
	h.advance(5 * time.Minute)
	report(hostA, sr4("swp1", 0.04, "rx low alarm lane 3"), dac) // same flag: no second event
	if n := h.count(`SELECT COUNT(*) FROM optic_sample`); n != 4 {
		t.Errorf("samples = %d, want 2 hours x 2 optics", n)
	}
	if got := kinds(); len(got) != 3 || got[2] != EventOpticAlarm {
		t.Errorf("events after the flag = %v", got)
	}
	probs, _ := h.s.ListOptics(h.ctx, "storage1", true, false)
	if len(probs) != 1 || probs[0].Optic.Serial != "MT1918FT01234" || len(probs[0].Problems) != 1 || probs[0].Problems[0] != "rx low alarm lane 3" {
		t.Errorf("problems = %+v", probs)
	}

	// Moved to another port, then to another host.
	h.advance(time.Hour)
	report(hostA, sr4("swp2", 0.75), dac)
	h.advance(time.Hour)
	report(hostB, sr4("swp4", 0.75))
	got := kinds()
	if len(got) != 5 || got[3] != EventOpticMoved || got[4] != EventOpticMoved {
		t.Fatalf("events after moves = %v", got)
	}
	d, err := h.s.GetOptic(h.ctx, "MT1918", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Row.Hostname != "backup" || d.Row.Optic.Port != "swp4" || len(d.Placements) != 3 || d.Placements[0].Port != "swp4" || d.Placements[1].EndReason != "moved" || d.Placements[2].EndReason != "port" {
		t.Errorf("detail = %+v placements %+v", d.Row, d.Placements)
	}
	if len(d.Events) != 4 || d.Events[0].Optic != "MT1918FT01234" || !strings.Contains(d.Events[0].Detail, `"from_host":"storage1"`) {
		t.Errorf("optic events = %+v", d.Events)
	}

	// Gone from host A's report: the DAC vanishes; the SR4 is not A's any more.
	h.advance(time.Hour)
	report(hostA)
	if got := kinds(); len(got) != 6 || got[5] != EventOpticVanished {
		t.Errorf("events after the DAC left = %v", got)
	}
	// An agent that did not look changes nothing.
	h.advance(time.Hour)
	h.submit(Report{Host: hostB, ObservedAt: h.now, Devices: []ReportDevice{devX}, Complete: true})
	if rows, _ := h.s.ListOptics(h.ctx, "backup", false, false); len(rows) != 1 {
		t.Errorf("after a report without optics = %+v", rows)
	}
	// The DAC comes back.
	h.advance(time.Hour)
	report(hostA, dac)
	if got := kinds(); got[len(got)-1] != EventOpticAppeared {
		t.Errorf("events after the DAC came back = %v", got)
	}
	all, _ := h.s.ListOptics(h.ctx, "", false, true)
	if len(all) != 2 {
		t.Errorf("all = %+v", all)
	}

	// Marked bad, resolved by host and port.
	ev, err := h.s.AnnotateOptic(h.ctx, "backup:swp4", StatusBad, "lane 3 dim", "tester")
	if err != nil || ev.Kind != EventOpticStatusChanged || ev.Optic != "MT1918FT01234" {
		t.Fatalf("AnnotateOptic = %+v, %v", ev, err)
	}
	if rows, _ := h.s.ListOptics(h.ctx, "", true, false); len(rows) != 1 || rows[0].Problems[0] != "marked bad" {
		t.Errorf("after marking = %+v", rows)
	}
	if _, err := h.s.ResolveOptic(h.ctx, "MT"); err == nil {
		t.Error("an ambiguous prefix resolved")
	}
	if _, err := h.s.ResolveOptic(h.ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
	evs, _ := h.s.ListEvents(h.ctx, EventFilter{Kinds: []string{EventOpticStatusChanged}})
	if len(evs) != 1 || evs[0].Optic != "MT1918FT01234" || evs[0].Hostname != "backup" {
		t.Errorf("ListEvents = %+v", evs)
	}
}
