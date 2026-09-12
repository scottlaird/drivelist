package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/scottlaird/drivelist/collect"
)

// KernelWatcher turns kernel log events into two things: an immediate
// re-inventory when a disk attaches or detaches (after a settle delay, so
// udev has named the device), and per-hour counts of everything else by
// drive, class and sense code, which the agent sends with its reports.
type KernelWatcher struct {
	agent  *Agent
	settle time.Duration
	log    *slog.Logger

	mu      sync.Mutex
	counts  map[bucketKey]*bucket
	pending bool
}

type bucketKey struct {
	Hour    time.Time // bucket start, UTC
	DevName string
	Addr    string // SCSI address or ATA port, for devices the name does not identify
	Class   string
	Code    string
}

type bucket struct {
	Count  int
	Sample string
}

// KernelCount is one aggregated row, ready to send.
type KernelCount struct {
	bucketKey
	Count  int
	Sample string
}

// NewKernelWatcher wires a watcher to an agent. settle is how long after
// an attach or detach to wait before reporting; the design's default is 5s.
func NewKernelWatcher(a *Agent, settle time.Duration, log *slog.Logger) *KernelWatcher {
	if settle <= 0 {
		settle = 5 * time.Second
	}
	if log == nil {
		log = a.log
	}
	return &KernelWatcher{agent: a, settle: settle, log: log, counts: map[bucketKey]*bucket{}}
}

// Run consumes events until ctx ends.
func (w *KernelWatcher) Run(ctx context.Context, events <-chan collect.KernelEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			w.handle(ctx, ev)
		}
	}
}

func (w *KernelWatcher) handle(ctx context.Context, ev collect.KernelEvent) {
	switch ev.Class {
	case collect.ClassAttach, collect.ClassDetach:
		w.log.Info("kernel reports a disk change; re-inventory scheduled", "class", ev.Class, "device", ev.DevName, "settle", w.settle)
		w.scheduleTrigger(ctx, ev.Class+" "+ev.DevName)
	}
	w.count(ev)
}

// scheduleTrigger fires one trigger after the settle delay. Several
// changes within the delay share one trigger.
func (w *KernelWatcher) scheduleTrigger(ctx context.Context, reason string) {
	w.mu.Lock()
	if w.pending {
		w.mu.Unlock()
		return
	}
	w.pending = true
	w.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.settle):
		}
		w.mu.Lock()
		w.pending = false
		w.mu.Unlock()
		w.agent.Trigger("kernel: " + reason)
	}()
}

func (w *KernelWatcher) count(ev collect.KernelEvent) {
	addr := ev.SCSIAddr
	if addr == "" {
		addr = ev.ATAPort
	}
	key := bucketKey{Hour: ev.At.UTC().Truncate(time.Hour), DevName: ev.DevName, Addr: addr, Class: ev.Class, Code: ev.Code()}
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.counts[key]
	if b == nil {
		b = &bucket{Sample: truncate(ev.Text, 300)}
		w.counts[key] = b
	}
	b.Count++
}

// Drain returns every bucket that ended before now and forgets it. The
// current hour stays until it is over, so a count is sent once, complete.
func (w *KernelWatcher) Drain(now time.Time) []KernelCount {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []KernelCount
	cutoff := now.UTC().Truncate(time.Hour)
	for k, b := range w.counts {
		if k.Hour.Before(cutoff) {
			out = append(out, KernelCount{bucketKey: k, Count: b.Count, Sample: b.Sample})
			delete(w.counts, k)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
