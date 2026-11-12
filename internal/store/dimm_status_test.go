package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDIMMIdentifiable(t *testing.T) {
	for _, tt := range []struct {
		serial string
		want   bool
	}{
		{"F41B398A", true},
		{"80CE042542F50C0000", true},
		{"0x0000ABCD", true},
		{"", false},
		{"   ", false},
		{"00000000", false},
		{"0x00000000", false},
		{"FFFFFFFF", false},
		{"SerNum0", false},
		{"Unknown", false},
		{"Not Specified", false},
		{"NO DIMM", false},
		{"To Be Filled By O.E.M.", false},
	} {
		if got := DIMMIdentifiable(tt.serial); got != tt.want {
			t.Errorf("DIMMIdentifiable(%q) = %v, want %v", tt.serial, got, tt.want)
		}
	}
}

// TestAnnotateDIMM: a module is marked by serial, prefix, PART/SERIAL or
// HOST:SLOT; the status shows in ListDIMMs and makes it a problem; it
// follows the module to another host; a module with a placeholder serial
// can't be marked.
func TestAnnotateDIMM(t *testing.T) {
	h := newHarness(t)
	dimms := []DIMM{
		{Slot: "DIMM 0 (P0 CHANNEL A)", Bank: "P0 CHANNEL A", SizeBytes: 16 << 30, Type: "DDR5", Part: "CBD48S40BS8MA-16", Serial: "F41B398A"},
		{Slot: "DIMM 0 (P0 CHANNEL B)", Bank: "P0 CHANNEL B", SizeBytes: 16 << 30, Type: "DDR5", Part: "CBD48S40BS8MA-16", Serial: "F31ADEAD"},
		{Slot: "DIMM 1 (P0 CHANNEL A)", Bank: "P0 CHANNEL A", SizeBytes: 16 << 30, Type: "DDR5", Part: "CMSX16", Serial: "00000000"},
	}
	r := Report{Host: hostA, ObservedAt: h.now, Devices: []ReportDevice{devX}, Complete: true, DIMMs: dimms}
	h.submit(r)

	ev, err := h.s.AnnotateDIMM(h.ctx, "F41B398A", StatusSuspect, "panicked; x265 output varies", "scott")
	if err != nil {
		t.Fatalf("AnnotateDIMM(serial) = %v", err)
	}
	if d := detail(t, ev); ev.Kind != EventDimmStatusChanged || ev.Hostname != "storage1" || d["previous"] != StatusOK || d["status"] != StatusSuspect || d["slot"] != "DIMM 0 (P0 CHANNEL A)" || ev.Source != "user:scott" {
		t.Errorf("event = %+v", ev)
	}
	// HOST:SLOT by unambiguous prefix, and a note.
	if _, err := h.s.AnnotateDIMM(h.ctx, "storage1:dimm 0 (p0 channel b", StatusSuspect, "", "scott"); err != nil {
		t.Fatalf("AnnotateDIMM(HOST:SLOT prefix) = %v", err)
	}
	if ev, err := h.s.AnnotateDIMM(h.ctx, "CBD48S40BS8MA-16/F31ADEAD", "", "pulled from a crashing box", "scott"); err != nil || ev.Kind != EventDimmNote {
		t.Fatalf("AnnotateDIMM(PART/SERIAL note) = %+v, %v", ev, err)
	}

	rows, err := h.s.ListDIMMs(h.ctx, "", true)
	if err != nil || len(rows) != 2 {
		t.Fatalf("problems = %+v, %v; want the two marked modules", rows, err)
	}
	for _, r := range rows {
		if r.Status != StatusSuspect || !r.Problem() {
			t.Errorf("%s: status %q, problem %v", r.DIMM.Serial, r.Status, r.Problem())
		}
	}
	all, _ := h.s.ListDIMMs(h.ctx, "", false)
	if len(all) != 3 || all[2].Status != StatusOK || all[2].Problem() {
		t.Errorf("unmarked module = %+v", all[len(all)-1])
	}

	for _, tt := range []struct {
		ref, status, note string
		want              error
		text              string
	}{
		{"storage1:DIMM 1", StatusBad, "", ErrNoSerial, ""},
		{"00000000", StatusBad, "", ErrNotFound, ""},
		{"NOPE", StatusBad, "", ErrNotFound, ""},
		{"storage1:DIMM 0", StatusBad, "", nil, "matches 2"},
		{"F41B398A", "broken", "", nil, "is not one of"},
		{"F41B398A", "", "", nil, "nothing to record"},
	} {
		_, err := h.s.AnnotateDIMM(h.ctx, tt.ref, tt.status, tt.note, "scott")
		if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) || (tt.text != "" && !strings.Contains(err.Error(), tt.text)) {
			t.Errorf("AnnotateDIMM(%q, %q) = %v, want %v %q", tt.ref, tt.status, err, tt.want, tt.text)
		}
	}

	// The module moves to another host: its status goes with it.
	h.advance(time.Hour)
	r.ObservedAt, r.DIMMs = h.now, dimms[1:]
	h.submit(r)
	h.submit(Report{Host: hostB, ObservedAt: h.now, Devices: []ReportDevice{devY}, Complete: true, DIMMs: dimms[:1]})
	rows, _ = h.s.ListDIMMs(h.ctx, "backup", false)
	if len(rows) != 1 || rows[0].Status != StatusSuspect {
		t.Errorf("backup's modules = %+v; want F41B398A still suspect", rows)
	}

	d, err := h.s.GetDIMM(h.ctx, "f41b")
	if err != nil {
		t.Fatalf("GetDIMM = %v", err)
	}
	if d.Status != StatusSuspect || len(d.Places) != 2 || d.Places[0].Hostname != "backup" || !d.Places[0].GoneAt.IsZero() || d.Places[1].Hostname != "storage1" || d.Places[1].GoneAt.IsZero() {
		t.Errorf("GetDIMM = %+v", d)
	}
	var kinds []string
	for _, e := range d.Events {
		kinds = append(kinds, e.Kind)
	}
	if got := strings.Join(kinds, " "); !strings.Contains(got, EventDimmStatusChanged) || strings.Contains(got, EventDimmNote) || strings.Count(got, EventDimmChanged) != 3 {
		// appeared on storage1, vanished from it, appeared on backup; the
		// note was about the other module.
		t.Errorf("GetDIMM events = %s", got)
	}

	// Marking it ok again records the previous status.
	if ev, err := h.s.AnnotateDIMM(h.ctx, "F41B398A", StatusOK, "memtest clean", "scott"); err != nil || detail(t, ev)["previous"] != StatusSuspect || ev.Hostname != "backup" {
		t.Errorf("mark ok = %+v, %v", ev, err)
	}
}

func detail(t *testing.T, e Event) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
		t.Fatalf("event detail %q: %v", e.Detail, err)
	}
	return d
}
