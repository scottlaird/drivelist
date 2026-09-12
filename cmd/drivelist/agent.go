package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/scottlaird/drivelist/collect"
	"github.com/scottlaird/drivelist/internal/agent"
	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
	"github.com/scottlaird/drivelist/internal/pb/drivelistv1/drivelistv1connect"
	"github.com/scottlaird/drivelist/internal/report"
)

// defaultStateDir holds the spool and status cache. It has to be writable by
// whoever runs the agent; a systemd unit creates it with StateDirectory=.
const defaultStateDir = "/var/lib/drivelist"

func newAgentCmd(cfg *clientConfig) *cobra.Command {
	var (
		interval, settle time.Duration
		stateDir         string
		kmsg             bool
	)
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Run the reporting daemon on this host",
		Long: `agent reports this host's inventory to the server every interval (the
server may adjust it), spools reports while the server is unreachable
and replays them in order when it is back, and writes the server's
status for each local drive to the status cache so the plain listing
can show it. Needs the agent token.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := cfg.collectorClient()
			if err != nil {
				return err
			}
			a, err := agent.New(agent.Config{
				Host:       report.Host(),
				Interval:   interval,
				SpoolDir:   stateDir + "/spool",
				StatusPath: stateDir + "/status.json",
			}, connectSender{client}, collectAll, slog.Default())
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if kmsg {
				startKernelWatcher(ctx, a, settle)
			}
			slog.Info("agent starting", "host", report.Host().Hostname, "interval", interval, "state", stateDir, "kmsg", kmsg)
			a.Run(ctx)
			return nil
		},
	}
	f := cmd.Flags()
	f.DurationVar(&interval, "interval", 5*time.Minute, "how often to report until the server says otherwise")
	f.StringVar(&stateDir, "state-dir", defaultStateDir, "directory for the spool and status cache")
	f.BoolVar(&kmsg, "kmsg", true, "follow the kernel log and report within seconds of a disk attaching or detaching")
	f.DurationVar(&settle, "kmsg-settle", 5*time.Second, "how long after a kernel disk change to wait for udev before reporting")
	return cmd
}

// startKernelWatcher follows /dev/kmsg in the background. A host where it
// cannot be opened (not root, no such file on macOS) logs once and runs
// without it.
func startKernelWatcher(ctx context.Context, a *agent.Agent, settle time.Duration) {
	r, err := collect.OpenKernelLog()
	if err != nil {
		slog.Warn("kernel log not available; running without attach/detach triggers", "err", err)
		return
	}
	events := make(chan collect.KernelEvent, 64)
	w := agent.NewKernelWatcher(a, settle, slog.Default())
	a.SetKernelWatcher(w)
	go w.Run(ctx, events)
	go func() {
		if err := collect.FollowKernelLog(ctx, r, time.Now, events); err != nil {
			slog.Error("kernel log follower stopped", "err", err)
		}
		close(events)
	}()
}

// connectSender adapts the generated client to agent.Sender.
type connectSender struct {
	client drivelistv1connect.CollectorClient
}

func (c connectSender) Report(ctx context.Context, req *pb.ReportInventoryRequest) (*pb.ReportInventoryResponse, error) {
	res, err := c.client.ReportInventory(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func (c connectSender) Kernel(ctx context.Context, req *pb.ReportKernelRequest) (*pb.ReportAck, error) {
	res, err := c.client.ReportKernel(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}
