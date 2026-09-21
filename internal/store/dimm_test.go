package store

import (
	"strings"
	"testing"
	"time"
)

// TestIngestDIMMs: modules are tracked by slot; counts that grow within a
// boot leave samples and one memory_errors event per module per day; a
// new boot restarts the counts; a serial change in a slot is a
// replacement; a hardware_error naming the module's EDAC location gets
// the slot.
func TestIngestDIMMs(t *testing.T) {
	h := newHarness(t)
	host := hostA
	host.BootID = "boot-1"
	dimms := func(ceB1 uint64) []DIMM {
		return []DIMM{
			{Slot: "DIMMA1", Bank: "P0_Node0_Channel0_Dimm1", SizeBytes: 32 << 30, Ranks: 2, Type: "DDR5", SpeedMTs: 5600, Part: "MB32G56U80M2R8", Serial: "802C0425", EDAC: "mc0/csrow2/ch0+mc0/csrow3/ch0", Mapping: "exact"},
			{Slot: "DIMMB1", Bank: "P0_Node0_Channel1_Dimm1", SizeBytes: 32 << 30, Ranks: 2, Type: "DDR5", SpeedMTs: 5600, Part: "MB32G56U80S2R8", Serial: "80CE0425", EDAC: "mc0/csrow2/ch1+mc0/csrow3/ch1", Mapping: "exact", CE: ceB1},
		}
	}
	r := Report{Host: host, ObservedAt: h.now, Devices: []ReportDevice{devX}, Complete: true, DIMMs: dimms(0)}
	h.submit(r)
	if got := h.sasEvents(EventDimmChanged); len(got) != 2 || !strings.Contains(got[0], `"change":"appeared"`) {
		t.Fatalf("dimm events on first sight = %v", got)
	}
	rows, err := h.s.ListDIMMs(h.ctx, "", false)
	if err != nil || len(rows) != 2 || rows[0].DIMM.Slot != "DIMMA1" || rows[0].Problem() {
		t.Fatalf("ListDIMMs = %+v, %v", rows, err)
	}

	// An hour on, B1 has counted 3600 corrected errors.
	h.advance(time.Hour)
	r.ObservedAt = h.now
	r.DIMMs = dimms(3600)
	h.submit(r)
	if n := h.count(`SELECT COUNT(*) FROM dimm_sample`); n != 1 {
		t.Errorf("samples = %d, want 1", n)
	}
	events := h.sasEvents(EventMemoryErrors)
	if len(events) != 1 || !strings.Contains(events[0], `"label":"DIMMB1"`) || !strings.Contains(events[0], `"grew":{"ce":3600,"ue":0}`) {
		t.Errorf("memory_errors = %v", events)
	}
	rows, _ = h.s.ListDIMMs(h.ctx, "storage1", true)
	if len(rows) != 1 || rows[0].DIMM.Slot != "DIMMB1" || rows[0].CEDay != 3600 || !rows[0].Problem() || rows[0].LastError.IsZero() {
		t.Errorf("problems = %+v", rows)
	}
	// More the same day: a sample, no second event.
	h.advance(time.Hour)
	r.ObservedAt = h.now
	r.DIMMs = dimms(7000)
	h.submit(r)
	if n := h.count(`SELECT COUNT(*) FROM dimm_sample`); n != 2 {
		t.Errorf("samples after second growth = %d, want 2", n)
	}
	if got := h.sasEvents(EventMemoryErrors); len(got) != 1 {
		t.Errorf("memory_errors after second growth = %d, want 1", len(got))
	}

	// A hardware_error from the kernel log on that module's location
	// names the slot.
	if _, err := h.s.IngestKernel(h.ctx, host, []KernelSample{{BucketStart: h.now, BucketSecs: 3600, Class: "hw_corrected", Code: "mc0/csrow3/ch1", Count: 9, Sample: "EDAC MC0: 1 CE on mc#0csrow#3channel#1"}}); err != nil {
		t.Fatal(err)
	}
	if got := h.sasEvents(EventHardwareError); len(got) != 1 || !strings.Contains(got[0], `"slot":"DIMMB1"`) || !strings.Contains(got[0], `"serial":"80CE0425"`) {
		t.Errorf("hardware_error = %v", got)
	}

	// The host reboots: counts restart at 12, which is not growth.
	h.advance(time.Hour)
	host.BootID = "boot-2"
	r.Host, r.ObservedAt = host, h.now
	r.DIMMs = dimms(12)
	h.submit(r)
	if n := h.count(`SELECT COUNT(*) FROM dimm_sample`); n != 2 {
		t.Errorf("samples after reboot = %d, want still 2", n)
	}

	// B1 is replaced: same slot, new serial. One event, and its counts
	// start from its own.
	h.advance(time.Hour)
	r.ObservedAt = h.now
	r.DIMMs = dimms(0)
	r.DIMMs[1].Serial, r.DIMMs[1].Part = "NEW00001", "MB32G56U80M2R8"
	h.submit(r)
	changed := h.sasEvents(EventDimmChanged)
	if len(changed) != 3 || !strings.Contains(changed[2], `"change":"replaced"`) || !strings.Contains(changed[2], `"from_serial":"80CE0425"`) {
		t.Errorf("dimm_changed after replacement = %v", changed)
	}
	rows, _ = h.s.ListDIMMs(h.ctx, "", false)
	if len(rows) != 2 || rows[1].DIMM.Serial != "NEW00001" || rows[1].DIMM.CE != 0 {
		t.Errorf("after replacement = %+v", rows)
	}

	// A1 is pulled: vanished.
	h.advance(time.Hour)
	r.ObservedAt = h.now
	r.DIMMs = r.DIMMs[1:]
	h.submit(r)
	if got := h.sasEvents(EventDimmChanged); len(got) != 4 || !strings.Contains(got[3], `"change":"vanished"`) || !strings.Contains(got[3], `"slot":"DIMMA1"`) {
		t.Errorf("dimm_changed after removal = %v", got)
	}
	if rows, _ := h.s.ListDIMMs(h.ctx, "", false); len(rows) != 1 {
		t.Errorf("present after removal = %+v", rows)
	}
	// A report with no modules changes nothing.
	h.advance(time.Hour)
	r.ObservedAt = h.now
	r.DIMMs = nil
	h.submit(r)
	if rows, _ := h.s.ListDIMMs(h.ctx, "", false); len(rows) != 1 {
		t.Errorf("after an empty report = %+v", rows)
	}
}
