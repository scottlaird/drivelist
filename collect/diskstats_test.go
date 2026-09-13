package collect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const diskstats = `   8       0 sda 1000 10 80000 5000 2000 20 160000 12000 3 9000 17500 0 0 0 0 0 0
   8       1 sda1 900 10 70000 4500 1900 20 150000 11000 0 8000 15500 0 0 0 0 0 0
 259       0 nvme0n1 500 0 40000 100 700 0 56000 300 1 350 420 0 0 0 0 0 0
 259       1 nvme0n1p1 100 0 8000 20 100 0 8000 40 0 50 60 0 0 0 0 0 0
   7       0 loop0 10 0 80 0 0 0 0 0 0 0 0
   9     127 md127 0 0 0 0 0 0 0 0 0 0 0
`

func TestParseDiskStats(t *testing.T) {
	got, err := ParseDiskStats(strings.NewReader(diskstats))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "sda" || got[1].Name != "nvme0n1" {
		t.Fatalf("devices = %+v", got)
	}
	sda := got[0]
	if sda.Reads != 1000 || sda.SectorsRead != 80000 || sda.ReadMs != 5000 || sda.Writes != 2000 || sda.SectorsWrite != 160000 ||
		sda.WriteMs != 12000 || sda.InFlight != 3 || sda.IOMs != 9000 || sda.WeightedIOMs != 17500 {
		t.Errorf("sda = %+v", sda)
	}
}

func TestDelta(t *testing.T) {
	prev := DiskStat{Name: "sda", Reads: 1000, SectorsRead: 80000, ReadMs: 5000, Writes: 2000, SectorsWrite: 160000, WriteMs: 12000, IOMs: 9000, WeightedIOMs: 17500}
	cur := DiskStat{Name: "sda", Reads: 1100, SectorsRead: 88000, ReadMs: 5800, Writes: 2050, SectorsWrite: 164000, WriteMs: 12500, IOMs: 39000, WeightedIOMs: 20000}
	d, ok := Delta(prev, cur, time.Minute)
	if !ok {
		t.Fatal("delta not ok")
	}
	if d.Reads != 100 || d.ReadBytes != 8000*512 || d.Writes != 50 || d.WriteBytes != 4000*512 || d.ReadMs != 800 || d.WriteMs != 500 || d.IOMs != 30000 || d.WeightedMs != 2500 {
		t.Errorf("delta = %+v", d)
	}
	if d.RAwait() != 8 || d.WAwait() != 10 || d.Util() != 0.5 {
		t.Errorf("r_await=%v w_await=%v util=%v", d.RAwait(), d.WAwait(), d.Util())
	}
	// Counter went backwards (reboot): unusable.
	if _, ok := Delta(cur, prev, time.Minute); ok {
		t.Error("backwards counters accepted")
	}
	// Idle: zero rates, not NaN.
	idle, _ := Delta(cur, cur, time.Minute)
	if idle.RAwait() != 0 || idle.Util() != 0 {
		t.Errorf("idle = %+v", idle)
	}
}

func TestDropGlitches(t *testing.T) {
	uptime := 14 * 24 * time.Hour // 1.2e9 ms
	prev := DiskStat{Name: "nvme0n1", Reads: 100, ReadMs: 10, Writes: 1000, WriteMs: 300, WeightedIOMs: 310}
	// Writes took the uptime plus a normal 300 ms; reads are honest.
	cur := DiskStat{Name: "nvme0n1", Reads: 200, ReadMs: 20, Writes: 2800, WriteMs: 300 + uint64(uptime.Milliseconds()) + 300, WeightedIOMs: 310 + uint64(uptime.Milliseconds()) + 310}
	d, _ := Delta(prev, cur, time.Minute)
	if n := d.DropGlitches(uptime); n != 1 || d.Glitches != 1 {
		t.Fatalf("DropGlitches = %d, Glitches = %d; want 1", n, d.Glitches)
	}
	if d.Writes != 1800 || d.AwaitWrites != 0 || d.WriteMs != 0 || d.WeightedMs != 0 {
		t.Errorf("write side after drop = %+v", d)
	}
	if d.Reads != 100 || d.AwaitReads != 100 || d.ReadMs != 10 || d.RAwait() != 0.1 || d.WAwait() != 0 {
		t.Errorf("read side changed = %+v", d)
	}

	// The same jump with under an hour of uptime is left alone: it could be
	// a deep queue.
	d, _ = Delta(prev, cur, time.Minute)
	if n := d.DropGlitches(30 * time.Minute); n != 0 || d.AwaitWrites != 1800 {
		t.Errorf("early-uptime drop = %d, AwaitWrites = %d", n, d.AwaitWrites)
	}

	// A busy hour on a deep queue: 256 requests in flight for the whole
	// minute is 15.4e6 ms, far under a day of uptime.
	busy := DiskStat{Name: "sda", Writes: 100000, WriteMs: 256 * 60 * 1000}
	d, _ = Delta(DiskStat{Name: "sda"}, busy, time.Minute)
	if n := d.DropGlitches(24 * time.Hour); n != 0 {
		t.Errorf("busy interval rejected: %+v", d)
	}
}

func TestReadUptime(t *testing.T) {
	f := filepath.Join(t.TempDir(), "uptime")
	os.WriteFile(f, []byte("1209545.48 9500000.12\n"), 0o644)
	got, err := ReadUptime(f)
	if err != nil || got != 1209545480*time.Millisecond {
		t.Errorf("ReadUptime = %v, %v", got, err)
	}
	if _, err := ReadUptime(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing file: no error")
	}
}
