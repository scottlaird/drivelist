package collect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureRunner serves smartctl output from testdata/smart by device name;
// with -d it serves the sat variant for sdu only.
func fixtureRunner(t *testing.T) SmartRunner {
	t.Helper()
	return func(_ context.Context, args ...string) ([]byte, int, error) {
		dev := strings.TrimPrefix(args[len(args)-1], "/dev/")
		var dt string
		for i, a := range args {
			if a == "-d" && i+1 < len(args) {
				dt = args[i+1]
			}
		}
		file := map[string]string{"sdc": "sata", "sdag": "sas", "nvme0n1": "nvme", "sdz": "standby", "sdu": "usb-unknown"}[dev]
		if dev == "sdu" && dt == "sat" {
			file = "sata"
		}
		if file == "" {
			return nil, 0, errors.New("no such fixture")
		}
		b, err := os.ReadFile(filepath.Join("testdata", "smart", file+".json"))
		if err != nil {
			t.Fatal(err)
		}
		code := 0
		if file == "standby" {
			code = 2
		}
		if file == "usb-unknown" {
			code = 1
		}
		return b, code, nil
	}
}

func u64(v uint64) *uint64 { return &v }

func TestSampleSmartDialects(t *testing.T) {
	run := fixtureRunner(t)
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)

	sata, dt := SampleSmart(context.Background(), run, "sdc", "", now)
	if sata.Skipped != "" || dt != "" || sata.Summary == nil || sata.Raw == nil {
		t.Fatalf("sata sample = %+v (type %q)", sata, dt)
	}
	s := sata.Summary
	if s.Protocol != "ATA" || !*s.Healthy || *s.PowerOnHours != 12345 || *s.TempC != 31 ||
		*s.Reallocated != 0 || *s.Pending != 2 || *s.Uncorrectable != 0 || *s.CRCErrors != 3 ||
		*s.WriteBytes != 40000000000*512 || s.ReadBytes != nil || *s.PercentUsed != 4 ||
		s.SelftestLast != "short: completed without error @ 12000h" {
		t.Errorf("sata summary = %+v selftest=%q", *s, s.SelftestLast)
	}

	sas, _ := SampleSmart(context.Background(), run, "sdag", "", now)
	s = sas.Summary
	if s == nil || s.Protocol != "SCSI" || *s.Healthy || *s.PowerOnHours != 41120 || *s.Reallocated != 7 || *s.Uncorrectable != 3 ||
		s.Pending != nil || s.CRCErrors != nil || *s.ReadBytes != 123456789000000 || *s.WriteBytes != 45678100000000 ||
		s.SelftestLast != "short: completed @ 40000h" {
		t.Errorf("sas summary = %+v", s)
	}

	nvme, _ := SampleSmart(context.Background(), run, "nvme0n1", "", now)
	s = nvme.Summary
	if s == nil || s.Protocol != "NVMe" || !*s.Healthy || *s.PowerOnHours != 20000 || *s.TempC != 35 || *s.PercentUsed != 4 ||
		*s.Uncorrectable != 0 || *s.ReadBytes != 123456789*512000 || *s.WriteBytes != 98765432*512000 || s.Reallocated != nil {
		t.Errorf("nvme summary = %+v", s)
	}
}

func TestSampleSmartSkips(t *testing.T) {
	run := fixtureRunner(t)
	now := time.Now()
	standby, _ := SampleSmart(context.Background(), run, "sdz", "", now)
	if standby.Skipped != SkipStandby || standby.Summary != nil || standby.Raw != nil {
		t.Errorf("standby = %+v", standby)
	}
	// Unknown USB bridge: plain fails, -d sat works, and sat is remembered.
	usb, dt := SampleSmart(context.Background(), run, "sdu", "", now)
	if usb.Skipped != "" || usb.Summary == nil || dt != "sat" {
		t.Errorf("usb = %+v type %q", usb, dt)
	}
	// With the type remembered, one run.
	calls := 0
	counting := func(ctx context.Context, args ...string) ([]byte, int, error) {
		calls++
		return run(ctx, args...)
	}
	SampleSmart(context.Background(), counting, "sdu", "sat", now)
	if calls != 1 {
		t.Errorf("remembered type still took %d runs", calls)
	}
	// Nothing works.
	none, _ := SampleSmart(context.Background(), func(context.Context, ...string) ([]byte, int, error) {
		return []byte(`{"smartctl":{"exit_status":2,"messages":[{"string":"Smartctl open device: /dev/sdq failed: No such device","severity":"error"}]}}`), 2, nil
	}, "sdq", "", now)
	if none.Skipped != SkipUnsupported {
		t.Errorf("unusable device = %+v", none)
	}
	// Timeout.
	slow := func(ctx context.Context, args ...string) ([]byte, int, error) {
		<-ctx.Done()
		return nil, 0, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	timedOut, _ := SampleSmart(ctx, slow, "sdx", "scsi", now)
	if timedOut.Skipped != SkipTimeout {
		t.Errorf("timeout = %+v", timedOut)
	}
	// Garbage.
	garbage, _ := SampleSmart(context.Background(), func(context.Context, ...string) ([]byte, int, error) {
		return []byte("not json"), 0, nil
	}, "sdy", "scsi", now)
	if !strings.HasPrefix(garbage.Skipped, "error:") {
		t.Errorf("garbage = %+v", garbage)
	}
}

func TestGigabytesToBytes(t *testing.T) {
	if b, ok := gigabytesToBytes("123456.789"); !ok || b != 123456789000000 {
		t.Errorf("gigabytesToBytes = %d %v", b, ok)
	}
	if _, ok := gigabytesToBytes(""); ok {
		t.Error("empty string parsed")
	}
}
