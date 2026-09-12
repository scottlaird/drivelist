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
)

// Sender posts one report. The connect client satisfies it through
// connectSender; tests use a fake.
type Sender interface {
	Report(ctx context.Context, req *pb.ReportInventoryRequest) (*pb.ReportInventoryResponse, error)
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
	a := &Agent{cfg: cfg, send: send, collect: collect, now: time.Now, log: log, trigger: make(chan string, 1)}
	if cfg.SpoolDir != "" {
		sp, err := openSpool(cfg.SpoolDir, cfg.MaxSpool)
		if err != nil {
			return nil, err
		}
		a.spool = sp
	}
	return a, nil
}

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
	for {
		select {
		case <-ctx.Done():
			return
		case reason := <-a.trigger:
			if !timer.Stop() {
				<-timer.C
			}
			a.cycle(ctx, reason, &interval)
		case <-timer.C:
			a.cycle(ctx, "tick", &interval)
		}
		timer.Reset(interval)
	}
}

// cycle collects, drains the spool, and sends. On a transport failure the
// report joins the spool. interval is updated from the server's config.
func (a *Agent) cycle(ctx context.Context, reason string, interval *time.Duration) {
	inv, collectErr := a.collect()
	if collectErr != nil {
		a.log.Error("collect failed; reporting as incomplete", "err", collectErr)
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
	if a.cfg.StatusPath != "" {
		if err := WriteStatusCache(a.cfg.StatusPath, a.now(), res.GetStatuses()); err != nil {
			a.log.Error("status cache write failed", "path", a.cfg.StatusPath, "err", err)
		}
	}
}
