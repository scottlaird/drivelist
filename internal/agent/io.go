package agent

import (
	"context"
	"sort"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/scottlaird/drivelist/collect"
	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
)

// IOConfig turns on I/O sampling.
type IOConfig struct {
	Interval time.Duration                      // between /proc/diskstats readings. 0: 60s
	Read     func() ([]collect.DiskStat, error) // nil: /proc/diskstats
	Uptime   func() (time.Duration, error)      // nil: /proc/uptime; feeds collect.IODelta.DropGlitches
}

// ioState accumulates 60 s deltas into hourly buckets per device and
// remembers the worst sub-sample in each.
type ioState struct {
	cfg        IOConfig
	mu         sync.Mutex
	prev       map[string]collect.DiskStat
	prevAt     time.Time
	prevUptime time.Duration // 0 when unknown: no glitch rejection
	open       map[ioKey]*ioBucket
}

type ioKey struct {
	Hour time.Time
	Dev  string
}

type ioBucket struct {
	collect.IODelta
	rMax, wMax, uMax float64
}

// maxIOBuckets bounds what is kept per device while the server is
// unreachable: two days of hours.
const maxIOBuckets = 48

// EnableIO turns on I/O sampling: a reading every interval, folded into the
// current hour's bucket per device; completed hours are sent after the
// next reading.
func (a *Agent) EnableIO(cfg IOConfig) {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.Read == nil {
		cfg.Read = func() ([]collect.DiskStat, error) { return collect.ReadDiskStats("/proc/diskstats") }
	}
	if cfg.Uptime == nil {
		cfg.Uptime = func() (time.Duration, error) { return collect.ReadUptime("/proc/uptime") }
	}
	a.io = &ioState{cfg: cfg, prev: map[string]collect.DiskStat{}, open: map[ioKey]*ioBucket{}}
}

// ioSample takes one reading, folds it in, and sends any hour that has
// ended.
func (a *Agent) ioSample(ctx context.Context) {
	st := a.io
	if st == nil {
		return
	}
	now := a.now()
	stats, err := st.cfg.Read()
	if err != nil {
		a.log.Warn("diskstats unreadable", "err", err)
		return
	}
	uptime, err := st.cfg.Uptime()
	if err != nil {
		a.log.Warn("uptime unreadable; not rejecting glitched counters", "err", err)
		uptime = 0
	}
	st.mu.Lock()
	hour := now.UTC().Truncate(time.Hour)
	if !st.prevAt.IsZero() {
		interval := now.Sub(st.prevAt)
		for _, cur := range stats {
			prev, ok := st.prev[cur.Name]
			if !ok {
				continue
			}
			d, ok := collect.Delta(prev, cur, interval)
			if !ok {
				continue
			}
			if n := d.DropGlitches(st.prevUptime); n > 0 {
				a.log.Warn("diskstats time counter jumped by the uptime; latency for the interval dropped", "device", cur.Name, "counters", n)
			}
			key := ioKey{Hour: hour, Dev: cur.Name}
			b := st.open[key]
			if b == nil {
				b = &ioBucket{IODelta: collect.IODelta{Name: cur.Name}}
				st.open[key] = b
			}
			b.Reads += d.Reads
			b.Writes += d.Writes
			b.ReadBytes += d.ReadBytes
			b.WriteBytes += d.WriteBytes
			b.ReadMs += d.ReadMs
			b.WriteMs += d.WriteMs
			b.IOMs += d.IOMs
			b.WeightedMs += d.WeightedMs
			b.AwaitReads += d.AwaitReads
			b.AwaitWrites += d.AwaitWrites
			b.Glitches += d.Glitches
			b.Interval += interval
			b.rMax = max(b.rMax, d.RAwait())
			b.wMax = max(b.wMax, d.WAwait())
			b.uMax = max(b.uMax, d.Util())
		}
	}
	st.prev = map[string]collect.DiskStat{}
	for _, s := range stats {
		st.prev[s.Name] = s
	}
	st.prevAt = now
	st.prevUptime = uptime

	// Completed hours, oldest first.
	var done []ioKey
	for k := range st.open {
		if k.Hour.Before(hour) {
			done = append(done, k)
		}
	}
	sort.Slice(done, func(i, j int) bool {
		if done[i].Hour.Equal(done[j].Hour) {
			return done[i].Dev < done[j].Dev
		}
		return done[i].Hour.Before(done[j].Hour)
	})
	req := &pb.ReportIORequest{Host: a.cfg.Host}
	var sent []ioKey
	for _, k := range done {
		b := st.open[k]
		id, ok := a.ids.lookup(k.Dev, "")
		if !ok {
			delete(st.open, k) // a device no inventory named: nothing to attribute it to
			continue
		}
		req.Samples = append(req.Samples, &pb.IOSample{
			Identity: id, DevName: k.Dev, BucketStart: timestamppb.New(k.Hour), BucketSecs: uint32(b.Interval.Seconds()),
			Reads: b.Reads, Writes: b.Writes, ReadBytes: b.ReadBytes, WriteBytes: b.WriteBytes,
			ReadMs: b.ReadMs, WriteMs: b.WriteMs, IoMs: b.IOMs, WeightedMs: b.WeightedMs,
			RAwaitMaxMs: b.rMax, WAwaitMaxMs: b.wMax, UtilMax: b.uMax,
			AwaitReads: b.AwaitReads, AwaitWrites: b.AwaitWrites, Glitches: uint32(b.Glitches),
		})
		sent = append(sent, k)
	}
	st.mu.Unlock()
	if len(req.Samples) == 0 {
		return
	}
	if _, err := a.send.IO(ctx, req); err != nil {
		a.log.Warn("io buckets not sent; keeping them", "err", err, "buckets", len(req.Samples))
		st.mu.Lock()
		st.pruneLocked()
		st.mu.Unlock()
		return
	}
	st.mu.Lock()
	for _, k := range sent {
		delete(st.open, k)
	}
	st.mu.Unlock()
	a.log.Info("io buckets sent", "buckets", len(req.Samples))
}

// pruneLocked drops the oldest buckets beyond the cap per device.
func (st *ioState) pruneLocked() {
	perDev := map[string][]ioKey{}
	for k := range st.open {
		perDev[k.Dev] = append(perDev[k.Dev], k)
	}
	for _, keys := range perDev {
		sort.Slice(keys, func(i, j int) bool { return keys[i].Hour.Before(keys[j].Hour) })
		for len(keys) > maxIOBuckets {
			delete(st.open, keys[0])
			keys = keys[1:]
		}
	}
}
