package collect

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// DiskStat is one line of /proc/diskstats for a whole disk: cumulative
// counters since boot. Sectors are 512 bytes whatever the device's block
// size; the kernel documents this.
type DiskStat struct {
	Name         string
	Reads        uint64 // completed
	Writes       uint64
	SectorsRead  uint64
	SectorsWrite uint64
	ReadMs       uint64 // time spent reading
	WriteMs      uint64
	InFlight     uint64 // I/Os in progress (a gauge, not a counter)
	IOMs         uint64 // time spent doing I/Os (the "util" clock)
	WeightedIOMs uint64 // sum over in-flight I/Os of their time
}

// ReadDiskStats parses /proc/diskstats, keeping whole disks drivelist
// tracks (sd*, nvmeXnY) and dropping partitions and everything else.
func ReadDiskStats(path string) ([]DiskStat, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseDiskStats(f)
}

// ParseDiskStats reads the /proc/diskstats format: major minor name, then
// the counters of Documentation/admin-guide/iostats.rst (11 in old
// kernels, 15 or 17 with discard and flush counters). Partition lines are
// skipped by name.
func ParseDiskStats(r io.Reader) ([]DiskStat, error) {
	var out []DiskStat
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 14 {
			continue
		}
		name := f[2]
		if !IsDiskName(name) {
			continue
		}
		n := make([]uint64, 11)
		for i := range n {
			v, err := strconv.ParseUint(f[3+i], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("diskstats: %s field %d: %w", name, 4+i, err)
			}
			n[i] = v
		}
		out = append(out, DiskStat{
			Name: name, Reads: n[0], SectorsRead: n[2], ReadMs: n[3],
			Writes: n[4], SectorsWrite: n[6], WriteMs: n[7],
			InFlight: n[8], IOMs: n[9], WeightedIOMs: n[10],
		})
	}
	return out, sc.Err()
}

// IODelta is the change between two DiskStat readings of one device over
// an interval, plus the derived rates iostat -x prints.
type IODelta struct {
	Name       string
	Interval   time.Duration
	Reads      uint64
	Writes     uint64
	ReadBytes  uint64
	WriteBytes uint64
	ReadMs     uint64
	WriteMs    uint64
	IOMs       uint64
	WeightedMs uint64
}

// RAwait is the mean read latency in ms over the interval, 0 with no reads.
func (d IODelta) RAwait() float64 { return ratio(d.ReadMs, d.Reads) }

// WAwait is the mean write latency in ms over the interval.
func (d IODelta) WAwait() float64 { return ratio(d.WriteMs, d.Writes) }

// Util is the fraction of the interval the device had I/O in flight.
func (d IODelta) Util() float64 {
	ms := d.Interval.Milliseconds()
	if ms <= 0 {
		return 0
	}
	return min(float64(d.IOMs)/float64(ms), 1)
}

func ratio(ms, n uint64) float64 {
	if n == 0 {
		return 0
	}
	return float64(ms) / float64(n)
}

// Delta computes the change from prev to cur. It returns false when a
// counter went backwards (a reboot, or a device that was re-created),
// in which case the interval is not usable.
func Delta(prev, cur DiskStat, interval time.Duration) (IODelta, bool) {
	sub := func(a, b uint64) (uint64, bool) {
		if b < a {
			return 0, false
		}
		return b - a, true
	}
	d := IODelta{Name: cur.Name, Interval: interval}
	var ok bool
	fields := []struct {
		dst  *uint64
		a, b uint64
	}{
		{&d.Reads, prev.Reads, cur.Reads}, {&d.Writes, prev.Writes, cur.Writes},
		{&d.ReadBytes, prev.SectorsRead, cur.SectorsRead}, {&d.WriteBytes, prev.SectorsWrite, cur.SectorsWrite},
		{&d.ReadMs, prev.ReadMs, cur.ReadMs}, {&d.WriteMs, prev.WriteMs, cur.WriteMs},
		{&d.IOMs, prev.IOMs, cur.IOMs}, {&d.WeightedMs, prev.WeightedIOMs, cur.WeightedIOMs},
	}
	for _, f := range fields {
		if *f.dst, ok = sub(f.a, f.b); !ok {
			return IODelta{}, false
		}
	}
	d.ReadBytes *= 512
	d.WriteBytes *= 512
	return d, true
}
