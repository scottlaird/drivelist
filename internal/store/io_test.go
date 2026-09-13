package store

import (
	"testing"
	"time"
)

func ioBucket(wwn, dev string, start time.Time, reads, readMs, ioMs uint64) IOSample {
	return IOSample{Identity: DriveIdentity{WWN: wwn}, DevName: dev, BucketStart: start, BucketSecs: 3600,
		Reads: reads, Writes: reads / 2, ReadBytes: reads * 4096, WriteBytes: reads * 2048, ReadMs: readMs, WriteMs: readMs / 2, IOMs: ioMs, WeightedMs: ioMs * 2,
		RAwaitMax: float64(readMs) / float64(reads) * 3, UtilMax: 0.9}
}

func TestIngestAndQueryIO(t *testing.T) {
	h := fleet(t)
	hour := h.now.Add(-3 * time.Hour).Truncate(time.Hour)
	samples := []IOSample{
		ioBucket("0x5000000000000001", "sda", hour, 1000, 8000, 900000),
		ioBucket("0x5000000000000001", "sda", hour.Add(time.Hour), 2000, 16000, 1800000),
		ioBucket("0x5000000000000099", "sdq", hour, 10, 10, 10), // unknown drive
	}
	n, err := h.s.IngestIO(h.ctx, hostA, samples)
	if err != nil || n != 2 {
		t.Fatalf("IngestIO = %d, %v; want 2", n, err)
	}
	// Resend replaces.
	samples[0].Reads = 1500
	h.s.IngestIO(h.ctx, hostA, samples[:1])
	d, got, err := h.s.IOSamples(h.ctx, "X1", hour.Add(-time.Hour))
	if err != nil || d.Serial != "X1" || len(got) != 2 {
		t.Fatalf("IOSamples = %+v, %v", got, err)
	}
	if got[0].BucketStart != hour.Add(time.Hour) || got[1].Reads != 1500 || got[1].RAwait() != 8000.0/1500 || got[0].Util() != 0.5 || got[0].Hostname != "storage1" {
		t.Errorf("samples = %+v", got)
	}
}

func TestCompareIO(t *testing.T) {
	h := fleet(t)
	hour := h.now.Add(-2 * time.Hour).Truncate(time.Hour)
	// X (raidz2 10 in tank) and Y (spare on backup) are placed; X is slow.
	h.s.IngestIO(h.ctx, hostA, []IOSample{ioBucket("0x5000000000000001", "sda", hour, 1000, 30000, 1800000)})
	h.s.IngestIO(h.ctx, hostB, []IOSample{ioBucket("0x5000000000000002", "sdq", hour, 1000, 10000, 360000)})
	rows, err := h.s.CompareIO(h.ctx, "", hour.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	byS := map[string]IOComparison{}
	for _, r := range rows {
		byS[r.Serial] = r
	}
	x, y := byS["X1"], byS["Y1"]
	if x.Group != "zfs > tank 1 > raidz2 10" || x.RAwait != 30 || x.Util != 0.5 || x.GroupSize != 1 || x.GroupRAwait != 30 {
		t.Errorf("X = %+v", x)
	}
	if y.Group != "zfs > spare tank" || y.RAwait != 10 || y.Util != 0.1 {
		t.Errorf("Y = %+v", y)
	}
	onlyA, err := h.s.CompareIO(h.ctx, "storage1", hour.Add(-time.Hour))
	if err != nil || len(onlyA) != 1 || onlyA[0].Serial != "X1" {
		t.Errorf("host filter = %+v, %v", onlyA, err)
	}
	// Median over three drives in one group.
	if vdevGroup(`["zfs > tank 1 > raidz2 10 > disk 100","mount > /x"]`) != "zfs > tank 1 > raidz2 10" || vdevGroup(`["mount > /x"]`) != "" {
		t.Error("vdevGroup")
	}
	if median([]float64{5, 1, 3}) != 3 || median([]float64{4, 1, 3, 2}) != 2.5 || median(nil) != 0 {
		t.Error("median")
	}
}

func TestRetainIO(t *testing.T) {
	h := fleet(t)
	old := h.now.Add(-10 * 24 * time.Hour).Truncate(24 * time.Hour)
	var samples []IOSample
	for i := 0; i < 24; i++ {
		samples = append(samples, ioBucket("0x5000000000000001", "sda", old.Add(time.Duration(i)*time.Hour), 100, 800, 36000))
	}
	recent := h.now.Add(-time.Hour).Truncate(time.Hour)
	samples = append(samples, ioBucket("0x5000000000000001", "sda", recent, 100, 800, 36000))
	h.s.IngestIO(h.ctx, hostA, samples)
	n, err := h.s.RetainIO(h.ctx, 7*24*time.Hour)
	if err != nil || n != 24 {
		t.Fatalf("RetainIO = %d, %v; want 24 hourly rows pruned", n, err)
	}
	if h.count(`SELECT COUNT(*) FROM io_sample`) != 1 || h.count(`SELECT COUNT(*) FROM io_daily`) != 1 {
		t.Errorf("after retention: %d hourly, %d daily", h.count(`SELECT COUNT(*) FROM io_sample`), h.count(`SELECT COUNT(*) FROM io_daily`))
	}
	_, got, _ := h.s.IOSamples(h.ctx, "X1", time.Time{})
	if len(got) != 2 || got[1].BucketSecs != 24*3600 || got[1].Reads != 2400 || got[1].RAwait() != 8 {
		t.Errorf("daily sample = %+v", got)
	}
	// Idempotent.
	if n, _ := h.s.RetainIO(h.ctx, 7*24*time.Hour); n != 0 {
		t.Errorf("second RetainIO pruned %d", n)
	}
}

func TestIOAwaitCoversOnlyAcceptedIntervals(t *testing.T) {
	h := fleet(t)
	hour := h.now.Add(-3 * time.Hour).Truncate(time.Hour)
	// A 0.4 agent that rejected two intervals: the hour's 1000 reads all
	// count for throughput, the 8000 ms of latency covers 900 of them.
	glitched := ioBucket("0x5000000000000001", "sda", hour, 1000, 8000, 900000)
	glitched.AwaitReads, glitched.AwaitWrites, glitched.Glitches = 900, 500, 2
	// A 0.3 agent leaves the await counts zero: every completion counts.
	old := ioBucket("0x5000000000000001", "sda", hour.Add(time.Hour), 2000, 16000, 1800000)
	if _, err := h.s.IngestIO(h.ctx, hostA, []IOSample{glitched, old}); err != nil {
		t.Fatal(err)
	}
	_, got, err := h.s.IOSamples(h.ctx, "X1", hour.Add(-time.Hour))
	if err != nil || len(got) != 2 {
		t.Fatalf("IOSamples = %+v, %v", got, err)
	}
	if got[1].AwaitReads != 900 || got[1].Glitches != 2 || got[1].RAwait() != 8000.0/900 {
		t.Errorf("glitched hour = %+v", got[1])
	}
	if got[0].AwaitReads != 2000 || got[0].AwaitWrites != 1000 || got[0].RAwait() != 8 {
		t.Errorf("old-agent hour = %+v", got[0])
	}
	rows, err := h.s.CompareIO(h.ctx, "storage1", hour.Add(-time.Hour))
	if err != nil || len(rows) != 1 {
		t.Fatalf("CompareIO = %+v, %v", rows, err)
	}
	if rows[0].RAwait != 24000.0/2900 || rows[0].Reads != 3000 || rows[0].Glitches != 2 {
		t.Errorf("comparison = %+v", rows[0])
	}
	if _, err := h.s.RetainIO(h.ctx, 0); err != nil {
		t.Fatal(err)
	}
	_, daily, err := h.s.IOSamples(h.ctx, "X1", hour.Add(-48*time.Hour))
	if err != nil || len(daily) < 1 {
		t.Fatalf("daily = %+v, %v", daily, err)
	}
	var awaitReads, glitches uint64
	for _, d := range daily {
		awaitReads += d.AwaitReads
		glitches += uint64(d.Glitches)
	}
	if awaitReads != 2900 || glitches != 2 {
		t.Errorf("daily rollup await_reads %d glitches %d", awaitReads, glitches)
	}
}
