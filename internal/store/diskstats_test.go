package store

import (
	"testing"
	"time"

	"github.com/scottlaird/drivelist/collect"
)

// TestIngestDiskStats: two pulls an hour apart make one bucket per placed
// device covering the hour; the first pull, a new boot, counters that
// went backwards, and an unplaced device make none.
func TestIngestDiskStats(t *testing.T) {
	h := newHarness(t)
	h.report(hostA, devX, devY)
	host := hostA
	host.BootID = "boot-1"
	stat := func(name string, reads, readMs, ioMs uint64) collect.DiskStat {
		return collect.DiskStat{Name: name, Reads: reads, Writes: reads * 2, SectorsRead: reads * 8, SectorsWrite: reads * 16, ReadMs: readMs, WriteMs: readMs * 2, IOMs: ioMs, WeightedIOMs: ioMs * 3}
	}
	first := []collect.DiskStat{stat("sda", 1000, 5000, 60000), stat("sdb", 500, 1000, 30000), stat("sdz", 9, 9, 9)}
	n, err := h.s.IngestDiskStats(h.ctx, host, h.now, 10*24*time.Hour, first)
	if err != nil || n != 0 {
		t.Fatalf("first pull = %d buckets, %v; want 0", n, err)
	}
	if got := h.count(`SELECT COUNT(*) FROM io_counter`); got != 2 {
		t.Errorf("counters kept = %d, want 2 (only placed devices)", got)
	}

	// An hour later: sda did 600 reads taking 3000 ms, busy 36 s of it.
	h.advance(time.Hour)
	second := []collect.DiskStat{stat("sda", 1600, 8000, 96000), stat("sdb", 500, 1000, 30000), stat("sdz", 10, 10, 10)}
	n, err = h.s.IngestDiskStats(h.ctx, host, h.now, 10*24*time.Hour+time.Hour, second)
	if err != nil || n != 2 {
		t.Fatalf("second pull = %d buckets, %v; want 2", n, err)
	}
	_, samples, err := h.s.IOSamples(h.ctx, "X1", h.now.Add(-2*time.Hour))
	if err != nil || len(samples) != 1 {
		t.Fatalf("X1 samples = %v, %v", samples, err)
	}
	k := samples[0]
	if k.BucketSecs != 3600 || !k.BucketStart.Equal(h.now.Add(-time.Hour)) || k.Reads != 600 || k.Writes != 1200 || k.ReadBytes != 600*8*512 || k.ReadMs != 3000 || k.AwaitReads != 600 || k.Glitches != 0 {
		t.Errorf("bucket = %+v", k)
	}
	if k.RAwait() != 5 || k.RAwaitMax != 5 || k.Util() != 0.01 || k.UtilMax != 0.01 {
		t.Errorf("rates: r_await %v (max %v), util %v (max %v)", k.RAwait(), k.RAwaitMax, k.Util(), k.UtilMax)
	}
	if _, ys, _ := h.s.IOSamples(h.ctx, "Y1", h.now.Add(-2*time.Hour)); len(ys) != 1 || ys[0].Reads != 0 {
		t.Errorf("an idle device still gets its (empty) bucket: %v", ys)
	}

	// A kernel that accounts a completion from time zero: read_ms jumps by
	// the uptime. The interval keeps its reads and loses its read latency;
	// the write side, honest, keeps its.
	h.advance(time.Hour)
	third := stat("sda", 1700, 8000, 100000)
	third.WriteMs = 16000 + 400
	third.ReadMs = 8000 + uint64((10*24*time.Hour + time.Hour).Milliseconds())
	if n, err := h.s.IngestDiskStats(h.ctx, host, h.now, 10*24*time.Hour+2*time.Hour, []collect.DiskStat{third}); err != nil || n != 1 {
		t.Fatalf("third pull = %d, %v", n, err)
	}
	_, samples, _ = h.s.IOSamples(h.ctx, "X1", h.now.Add(-90*time.Minute))
	if len(samples) != 1 || samples[0].Reads != 100 || samples[0].ReadMs != 0 || samples[0].AwaitReads != 0 || samples[0].WriteMs != 400 || samples[0].AwaitWrites != 200 || samples[0].Glitches != 1 {
		t.Errorf("glitched bucket = %+v", samples)
	}

	// The host reboots: counters restart low, and even a higher one is not
	// growth. No bucket, and the new counters are the baseline.
	h.advance(time.Hour)
	rebooted := host
	rebooted.BootID = "boot-2"
	fourth := []collect.DiskStat{stat("sda", 5000, 100, 100)}
	if n, err := h.s.IngestDiskStats(h.ctx, rebooted, h.now, 10*time.Minute, fourth); err != nil || n != 0 {
		t.Fatalf("pull after reboot = %d, %v; want 0", n, err)
	}
	h.advance(time.Hour)
	fifth := []collect.DiskStat{stat("sda", 5100, 200, 200)}
	if n, err := h.s.IngestDiskStats(h.ctx, rebooted, h.now, 70*time.Minute, fifth); err != nil || n != 1 {
		t.Fatalf("pull within the new boot = %d, %v; want 1", n, err)
	}
	// Counters that went backwards without a boot id change: no bucket.
	h.advance(time.Hour)
	if n, err := h.s.IngestDiskStats(h.ctx, rebooted, h.now, 130*time.Minute, []collect.DiskStat{stat("sda", 10, 1, 1)}); err != nil || n != 0 {
		t.Fatalf("pull with counters gone backwards = %d, %v; want 0", n, err)
	}
	// A pull after too long a gap: remembered, not bucketed.
	h.advance(3 * 24 * time.Hour)
	if n, err := h.s.IngestDiskStats(h.ctx, rebooted, h.now, 0, []collect.DiskStat{stat("sda", 20, 2, 2)}); err != nil || n != 0 {
		t.Fatalf("pull after three days = %d, %v; want 0", n, err)
	}
}
