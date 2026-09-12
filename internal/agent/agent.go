// Package agent is the daemon that reports a host's inventory to the fleet
// server on a schedule, spools reports the server could not take, and keeps
// the local status cache the plain listing reads.
package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/scottlaird/drivelist"
	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
	"github.com/scottlaird/drivelist/internal/report"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Sender posts reports. The connect client satisfies it through an
// adapter in the command; tests use a fake.
type Sender interface {
	Report(ctx context.Context, req *pb.ReportInventoryRequest) (*pb.ReportInventoryResponse, error)
	Kernel(ctx context.Context, req *pb.ReportKernelRequest) (*pb.ReportAck, error)
	Smart(ctx context.Context, req *pb.ReportSmartRequest) (*pb.ReportAck, error)
}

// Config is what the loop needs.
type Config struct {
	Host       *pb.HostIdentity
	Interval   time.Duration // between inventory reports; the server may change it
	SpoolDir   string        // where undeliverable reports wait; "" disables spooling
	MaxSpool   int           // spool entries kept before the oldest is dropped; 0 means 2000
	StatusPath string        // the status cache; "" disables it
}

// Agent runs the loop.
type Agent struct {
	cfg     Config
	send    Sender
	collect func() (*drivelist.Inventory, error)
	now     func() time.Time
	log     *slog.Logger
	spool   *spool
	trigger chan string

	kernel *KernelWatcher
	ids    identityMap

	smart    *smartState
	smartReq chan struct{}
}

// New wires an agent. collect is what produces each report's inventory.
func New(cfg Config, send Sender, collect func() (*drivelist.Inventory, error), log *slog.Logger) (*Agent, error) {
	if cfg.Host == nil {
		return nil, errors.New("agent: no host identity")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Minute
	}
	if cfg.MaxSpool <= 0 {
		cfg.MaxSpool = 2000
	}
	if log == nil {
		log = slog.Default()
	}
	a := &Agent{cfg: cfg, send: send, collect: collect, now: time.Now, log: log, trigger: make(chan string, 1), ids: newIdentityMap(), smartReq: make(chan struct{}, 1)}
	if cfg.SpoolDir != "" {
		sp, err := openSpool(cfg.SpoolDir, cfg.MaxSpool)
		if err != nil {
			return nil, err
		}
		a.spool = sp
	}
	return a, nil
}

// SetKernelWatcher makes the agent send the watcher's completed buckets
// after each successful report.
func (a *Agent) SetKernelWatcher(w *KernelWatcher) { a.kernel = w }

// Trigger asks for a report before the next tick, naming why. Several
// triggers before the loop gets to it collapse into one report.
func (a *Agent) Trigger(reason string) {
	select {
	case a.trigger <- reason:
	default:
	}
}

// Run reports once immediately, then on every tick or trigger, until ctx
// ends. It never returns an error for a failed report; those are spooled
// and logged.
func (a *Agent) Run(ctx context.Context) {
	interval := a.cfg.Interval
	a.cycle(ctx, "start", &interval)
	timer := time.NewTimer(interval)
	defer timer.Stop()

	// SMART runs on its own clock: a baseline pass right away, then every
	// smart interval, plus out-of-band passes for requested devices.
	smartTick := make(<-chan time.Time)
	var smartTimer *time.Timer
	if a.smart != nil {
		a.smartPass(ctx, "baseline", nil)
		smartTimer = time.NewTimer(a.smart.cfg.Interval)
		defer smartTimer.Stop()
		smartTick = smartTimer.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case reason := <-a.trigger:
			if !timer.Stop() {
				<-timer.C
			}
			a.cycle(ctx, reason, &interval)
			timer.Reset(interval)
		case <-timer.C:
			a.cycle(ctx, "tick", &interval)
			timer.Reset(interval)
		case <-smartTick:
			a.smartPass(ctx, "tick", nil)
			smartTimer.Reset(a.smart.cfg.Interval)
		case <-a.smartReq:
			a.smartPass(ctx, "request", a.smart.takePending())
		}
	}
}

// cycle collects, drains the spool, and sends. On a transport failure the
// report joins the spool. interval is updated from the server's config.
func (a *Agent) cycle(ctx context.Context, reason string, interval *time.Duration) {
	inv, collectErr := a.collect()
	if collectErr != nil {
		a.log.Error("collect failed; reporting as incomplete", "err", collectErr)
	}
	appeared := a.ids.update(inv, a.now())
	if len(appeared) > 0 && reason != "start" {
		a.RequestSmart(appeared...)
	}
	req := report.FromInventory(a.cfg.Host, inv, a.now(), collectErr)

	if a.spool != nil {
		if err := a.replay(ctx); err != nil {
			a.log.Warn("server unreachable; spooling report", "reason", reason, "err", err)
			a.enqueue(req)
			return
		}
	}
	res, err := a.send.Report(ctx, req)
	if err != nil {
		a.log.Warn("report failed; spooling", "reason", reason, "err", err)
		a.enqueue(req)
		return
	}
	a.applyResponse(res, req, reason, interval)
	a.sendKernel(ctx)
}

// sendKernel posts the kernel watcher's completed buckets, attributed to
// drives through the device names of recent inventories. Buckets for
// devices no inventory has named are dropped; on a transport failure the
// rest go back to the watcher for next time.
func (a *Agent) sendKernel(ctx context.Context) {
	if a.kernel == nil {
		return
	}
	counts := a.kernel.Drain(a.now())
	if len(counts) == 0 {
		return
	}
	req := &pb.ReportKernelRequest{Host: a.cfg.Host}
	for _, c := range counts {
		id, ok := a.ids.lookup(c.DevName, c.Addr)
		if !ok {
			a.log.Debug("kernel bucket for an unknown device dropped", "device", c.DevName, "addr", c.Addr, "class", c.Class, "count", c.Count)
			continue
		}
		req.Samples = append(req.Samples, &pb.KernelSample{
			Identity:    id,
			DevName:     c.DevName,
			BucketStart: timestamppb.New(c.Hour),
			BucketSecs:  3600,
			Class:       c.Class,
			ScsiCode:    c.Code,
			Count:       uint32(c.Count),
			Sample:      c.Sample,
		})
	}
	if len(req.Samples) == 0 {
		return
	}
	if _, err := a.send.Kernel(ctx, req); err != nil {
		a.log.Warn("kernel counts not sent; keeping them", "err", err)
		a.kernel.restore(counts)
		return
	}
	a.log.Info("kernel counts sent", "buckets", len(req.Samples))
}

func (a *Agent) enqueue(req *pb.ReportInventoryRequest) {
	if a.spool == nil {
		return
	}
	if err := a.spool.add(req); err != nil {
		a.log.Error("spool write failed; report lost", "err", err)
	}
}

// replay sends spooled reports oldest first, stopping at the first
// transport failure. A report the server rejects is dropped: it is either
// older than what the server already has or from a skewed clock, and
// keeping it would block everything behind it.
func (a *Agent) replay(ctx context.Context) error {
	for {
		req, remove, err := a.spool.oldest()
		if err != nil {
			return err
		}
		if req == nil {
			return nil
		}
		res, err := a.send.Report(ctx, req)
		if err != nil {
			return err
		}
		if !res.GetAccepted() {
			a.log.Warn("spooled report rejected; dropped", "observed", req.GetObservedAt().AsTime(), "reason", res.GetRejectReason())
		}
		if err := remove(); err != nil {
			return err
		}
	}
}

func (a *Agent) applyResponse(res *pb.ReportInventoryResponse, req *pb.ReportInventoryRequest, reason string, interval *time.Duration) {
	if !res.GetAccepted() {
		a.log.Error("report rejected", "reason", res.GetRejectReason())
		return
	}
	if res.GetChanged() {
		a.log.Info("inventory changed", "devices", len(req.GetDevices()), "trigger", reason)
	} else {
		a.log.Debug("heartbeat", "devices", len(req.GetDevices()), "trigger", reason)
	}
	if d := res.GetConfig().GetInventoryInterval(); d != nil && d.AsDuration() > 0 && d.AsDuration() != *interval {
		a.log.Info("server set the interval", "interval", d.AsDuration())
		*interval = d.AsDuration()
	}
	if d := res.GetConfig().GetSmartInterval(); d != nil && d.AsDuration() > 0 && a.smart != nil && d.AsDuration() != a.smart.cfg.Interval {
		a.log.Info("server set the smart interval", "interval", d.AsDuration())
		a.smart.cfg.Interval = d.AsDuration()
	}
	if a.cfg.StatusPath != "" {
		if err := WriteStatusCache(a.cfg.StatusPath, a.now(), res.GetStatuses()); err != nil {
			a.log.Error("status cache write failed", "path", a.cfg.StatusPath, "err", err)
		}
	}
}
