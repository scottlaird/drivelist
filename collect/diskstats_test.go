package collect

import (
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
