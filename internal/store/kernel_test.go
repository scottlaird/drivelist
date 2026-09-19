package store

import (
	"strings"
	"testing"
	"time"
)

func TestIngestKernel(t *testing.T) {
	h := fleet(t)
	hour := h.now.Truncate(time.Hour)
	samples := []KernelSample{
		{Identity: DriveIdentity{WWN: "0x5000000000000001"}, DevName: "sda", BucketStart: hour, BucketSecs: 3600, Class: "recovered", Code: "1:b:97", Count: 40, Sample: "sd 11:0:45:0: [sda] …"},
		{Identity: DriveIdentity{WWN: "0x5000000000000001"}, DevName: "sda", BucketStart: hour, BucketSecs: 3600, Class: "predictive_failure", Code: "1:5d:90", Count: 3, Sample: "sd … ASC=0x5d"},
		{Identity: DriveIdentity{WWN: "0x5000000000000099"}, DevName: "sdq", BucketStart: hour, BucketSecs: 3600, Class: "io_error", Count: 1}, // unknown drive
	}
	n, err := h.s.IngestKernel(h.ctx, hostA, samples)
	if err != nil || n != 2 {
		t.Fatalf("IngestKernel = %d, %v; want 2", n, err)
	}
	// Resend with a higher count: replaced, not added; no second warning.
	samples[1].Count = 5
	if n, err := h.s.IngestKernel(h.ctx, hostA, samples[:2]); err != nil || n != 2 {
		t.Fatalf("resend = %d, %v", n, err)
	}
	d, got, err := h.s.KernelSamples(h.ctx, "X1", hour.Add(-time.Hour))
	if err != nil || d.Serial != "X1" {
		t.Fatalf("KernelSamples: %v %v", d, err)
	}
	if len(got) != 2 || got[0].Class != "predictive_failure" || got[0].Count != 5 || got[1].Count != 40 || got[0].Hostname != "storage1" {
		t.Errorf("samples = %+v", got)
	}
	kinds, details := h.events("X1")
	warnings := 0
	for i, k := range kinds {
		if k == EventKernelWarning {
			warnings++
			if details[i]["class"] != "predictive_failure" || details[i]["code"] != "1:5d:90" {
				t.Errorf("kernel_warning detail = %v", details[i])
			}
		}
	}
	if warnings != 1 {
		t.Errorf("kernel_warning events = %d, want 1 (recovered does not warn; resend does not repeat)", warnings)
	}
	// Next day, another one.
	samples[1].BucketStart = hour.Add(24 * time.Hour)
	h.s.IngestKernel(h.ctx, hostA, samples[1:2])
	kinds, _ = h.events("X1")
	warnings = 0
	for _, k := range kinds {
		if k == EventKernelWarning {
			warnings++
		}
	}
	if warnings != 2 {
		t.Errorf("kernel_warning events after a day = %d, want 2", warnings)
	}
	if _, _, err := h.s.KernelSamples(h.ctx, "nope", time.Time{}); err == nil {
		t.Error("unknown drive accepted")
	}
}

// TestIngestHardwareError: a sample about the host itself (no drive) is a
// hardware_error event on the host, once per class, location and day.
func TestIngestHardwareError(t *testing.T) {
	h := fleet(t)
	hour := h.now.Truncate(time.Hour)
	samples := []KernelSample{
		{BucketStart: hour, BucketSecs: 3600, Class: "hw_corrected", Code: "mc0/csrow3/ch1", Count: 3, Sample: "EDAC MC0: 1 CE on mc#0csrow#3channel#1 (…)"},
		{BucketStart: hour, BucketSecs: 3600, Class: "hw_corrected", Code: "", Count: 3, Sample: "[Hardware Error]: Corrected error, no action required."},
		{BucketStart: hour.Add(time.Hour), BucketSecs: 3600, Class: "hw_corrected", Code: "mc0/csrow3/ch1", Count: 9, Sample: "EDAC MC0: 1 CE on mc#0csrow#3channel#1 (…)"},
		{BucketStart: hour.Add(time.Hour), BucketSecs: 3600, Class: "hw_uncorrected", Code: "mc0/csrow3/ch1", Count: 1, Sample: "EDAC MC0: 1 UE on mc#0csrow#3channel#1 (…)"},
	}
	n, err := h.s.IngestKernel(h.ctx, hostA, samples)
	if err != nil || n != 4 {
		t.Fatalf("IngestKernel = %d, %v; want 4", n, err)
	}
	var got []string
	rows, err := h.s.db.Query(`SELECT detail FROM event WHERE kind = ? AND host_id = (SELECT host_id FROM host WHERE hostname = 'storage1') AND drive_id IS NULL ORDER BY event_id`, EventHardwareError)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var d string
		rows.Scan(&d)
		got = append(got, d)
	}
	rows.Close()
	if len(got) != 3 {
		t.Fatalf("hardware_error events = %v, want the two corrected (by location) and the uncorrected", got)
	}
	if !strings.Contains(got[0], `"code":"mc0/csrow3/ch1"`) || !strings.Contains(got[0], `"count":3`) || !strings.Contains(got[2], `"class":"hw_uncorrected"`) {
		t.Errorf("details = %v", got)
	}
	if n := h.count(`SELECT COUNT(*) FROM kmsg_sample`); n != 0 {
		t.Errorf("host samples stored under a drive: %d rows", n)
	}
}
