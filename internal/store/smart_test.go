package store

import (
	"bytes"
	"compress/gzip"
	"testing"
	"time"
)

func gz(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

func b(v bool) *bool     { return &v }
func u(v uint64) *uint64 { return &v }

func TestIngestSmart(t *testing.T) {
	h := fleet(t)
	x := DriveIdentity{WWN: "0x5000000000000001"}
	t0 := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC) // midnight, so same-day samples stay on one day

	first := SmartSample{Identity: x, DevName: "sda", TS: t0, RawGz: gz(`{"first":true}`),
		Summary: &SmartSummary{Protocol: "SCSI", Healthy: b(true), PowerOnHours: u(41000), Reallocated: u(0), Uncorrectable: u(0)}}
	n, err := h.s.IngestSmart(h.ctx, hostA, []SmartSample{first})
	if err != nil || n != 1 {
		t.Fatalf("IngestSmart = %d, %v", n, err)
	}
	// Nothing worse yet: no warning. Reallocated grows: one warning that day.
	worse := first
	worse.TS = t0.Add(6 * time.Hour)
	worse.RawGz = nil
	worse.Summary = &SmartSummary{Protocol: "SCSI", Healthy: b(true), PowerOnHours: u(41006), Reallocated: u(7), Uncorrectable: u(0)}
	h.s.IngestSmart(h.ctx, hostA, []SmartSample{worse})
	again := worse
	again.TS = t0.Add(12 * time.Hour)
	again.Summary = &SmartSummary{Protocol: "SCSI", Healthy: b(true), PowerOnHours: u(41012), Reallocated: u(9), Uncorrectable: u(0)}
	h.s.IngestSmart(h.ctx, hostA, []SmartSample{again})
	kinds, details := h.events("X1")
	warnings := 0
	for i, k := range kinds {
		if k == EventSmartWarning {
			warnings++
			if r, _ := details[i]["reasons"].([]any); len(r) != 1 || r[0] != "reallocated 7" {
				t.Errorf("smart_warning detail = %v", details[i])
			}
		}
	}
	if warnings != 1 {
		t.Errorf("smart_warning events = %d, want 1 (once per day)", warnings)
	}
	// Next day the health verdict fails: another warning.
	failed := again
	failed.TS = t0.Add(30 * time.Hour)
	failed.RawGz = gz(`{"failed":true}`)
	failed.Summary = &SmartSummary{Protocol: "SCSI", Healthy: b(false), PowerOnHours: u(41030), Reallocated: u(9), Uncorrectable: u(0)}
	h.s.IngestSmart(h.ctx, hostA, []SmartSample{failed})
	// A skipped sample records the skip and nothing else.
	skipped := SmartSample{Identity: x, DevName: "sda", TS: t0.Add(36 * time.Hour), Skipped: "standby"}
	h.s.IngestSmart(h.ctx, hostA, []SmartSample{skipped})
	// Unknown drive: ignored.
	if n, _ := h.s.IngestSmart(h.ctx, hostA, []SmartSample{{Identity: DriveIdentity{WWN: "0x99"}, Summary: &SmartSummary{}}}); n != 0 {
		t.Errorf("unknown drive stored %d", n)
	}

	d, samples, raw, rawTS, err := h.s.SmartSamples(h.ctx, "X1", time.Time{}, true)
	if err != nil || d.Serial != "X1" {
		t.Fatalf("SmartSamples: %v %v", d, err)
	}
	if len(samples) != 5 || samples[0].Skipped != "standby" || samples[0].Summary != nil ||
		samples[1].Summary == nil || *samples[1].Summary.Healthy || !samples[1].HasRaw || samples[2].HasRaw ||
		*samples[4].Summary.PowerOnHours != 41000 || samples[4].Hostname != "storage1" || samples[4].DevName != "sda" {
		for i, s := range samples {
			t.Logf("%d: %+v summary=%+v", i, s, s.Summary)
		}
		t.Errorf("samples wrong")
	}
	if string(raw) != `{"failed":true}` || rawTS != failed.TS.UTC() {
		t.Errorf("raw = %q @ %v", raw, rawTS)
	}
	kinds, _ = h.events("X1")
	warnings = 0
	for _, k := range kinds {
		if k == EventSmartWarning {
			warnings++
		}
	}
	if warnings != 2 {
		t.Errorf("smart_warning events after the failed verdict = %d, want 2", warnings)
	}
}

func TestSmartRawRetention(t *testing.T) {
	h := fleet(t)
	x := DriveIdentity{WWN: "0x5000000000000001"}
	t0 := h.now.Add(-100 * 24 * time.Hour)
	for i := 0; i < RawKeep+5; i++ {
		h.s.IngestSmart(h.ctx, hostA, []SmartSample{{Identity: x, TS: t0.Add(time.Duration(i) * 24 * time.Hour), RawGz: gz(`{}`), Summary: &SmartSummary{Protocol: "ATA"}}})
	}
	if n := h.count(`SELECT COUNT(*) FROM smart_raw`); n != RawKeep+1 {
		t.Errorf("raw rows = %d, want %d (first + last %d)", n, RawKeep+1, RawKeep)
	}
	var oldest int64
	h.s.db.QueryRow(`SELECT MIN(ts) FROM smart_raw`).Scan(&oldest)
	if oldest != t0.Unix() {
		t.Errorf("first raw document was pruned")
	}
	if n := h.count(`SELECT COUNT(*) FROM smart_sample WHERE raw_id IS NOT NULL`); n != RawKeep+1 {
		t.Errorf("samples still pointing at raw = %d, want %d", n, RawKeep+1)
	}
}
