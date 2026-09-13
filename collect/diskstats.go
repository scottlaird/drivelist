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
	// AwaitReads and AwaitWrites are the completions ReadMs and WriteMs
	// account for. They equal Reads and Writes unless DropGlitches threw
	// the interval's latency away, in which case they are 0.
	AwaitReads  uint64
	AwaitWrites uint64
	Glitches    int // counters DropGlitches rejected
}

// RAwait is the mean read latency in ms over the interval, 0 with no reads.
func (d IODelta) RAwait() float64 { return ratio(d.ReadMs, d.AwaitReads) }

// WAwait is the mean write latency in ms over the interval.
func (d IODelta) WAwait() float64 { return ratio(d.WriteMs, d.AwaitWrites) }

// glitchFraction is how much of the uptime a time counter must jump by in
// one interval to be rejected. A completion accounted from time zero adds
// the uptime at that moment, which is at least the uptime at the previous
// reading; the margin covers a clock that counts slightly slower than
// /proc/uptime, and nothing else, because an honest interval cannot come
// close: it is bounded by the queue depth times the interval.
const glitchFraction = 0.95

// DropGlitches rejects time counters that jumped by about the host's
// uptime in one interval, which is what a kernel that accounts an I/O from
// a zero start time produces (6.18.38 to 6.18.39, 7.1.3 to 7.1.4, and
// the distribution kernels that took the same patch). A rejected side
// keeps its completions and bytes but loses its latency: the ms sum goes
// to 0 and so does the completion count RAwait or WAwait would divide by,
// so the interval simply does not contribute to the hour's average. The
// weighted sum is dropped with either side, since it is built from the
// same per-request durations. It returns how many counters were rejected.
//
// uptime is the host's uptime at the previous reading. Below an hour of
// uptime nothing is rejected: a busy device can legitimately accumulate
// minutes of queue time per minute, and until the uptime dwarfs that the
// test cannot tell the two apart. The glitch is small then anyway.
func (d *IODelta) DropGlitches(uptime time.Duration) int {
	if uptime < time.Hour {
		return 0
	}
	limit := uint64(float64(uptime.Milliseconds()) * glitchFraction)
	n := 0
	if d.ReadMs >= limit {
		d.ReadMs, d.AwaitReads = 0, 0
		n++
	}
	if d.WriteMs >= limit {
		d.WriteMs, d.AwaitWrites = 0, 0
		n++
	}
	if n > 0 || d.WeightedMs >= limit {
		d.WeightedMs = 0
	}
	d.Glitches = n
	return n
}

// ReadUptime parses /proc/uptime: seconds since boot, then idle time.
func ReadUptime(path string) (time.Duration, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b))
	if len(f) < 1 {
		return 0, fmt.Errorf("uptime: %q: no fields", path)
	}
	secs, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, fmt.Errorf("uptime: %q: %w", path, err)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

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
	d.AwaitReads, d.AwaitWrites = d.Reads, d.Writes
	return d, true
}
