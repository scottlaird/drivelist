package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/scottlaird/drivelist/collect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/scottlaird/drivelist/hardware"
	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
	"github.com/scottlaird/drivelist/internal/report"
)

// fleetCommands are the subcommands that talk to the server. cfg is shared
// so --server and --json are persistent flags on the root.
func fleetCommands(cfg *clientConfig) []*cobra.Command {
	return []*cobra.Command{
		newReportCmd(cfg),
		newHostsCmd(cfg),
		newDrivesCmd(cfg),
		newSmartCmd(cfg),
		newDriveCmd(cfg),
		newEventsCmd(cfg),
		newMissingCmd(cfg),
		newIOCmd(cfg),
		newHostCmd(cfg),
		newAdminCmd(cfg),
		newEnclosuresCmd(cfg),
		newEnclosureCmd(cfg),
		newHardwareCmd(cfg),
	}
}

func newAdminCmd(cfg *clientConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Server maintenance",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "ingest FILE",
		Short: "Submit a report written by 'report --output' on another host",
		Long: `ingest submits a report bundle (an inventory report and, if it has one,
a SMART pass) as the host that wrote it. FILE is - for standard input.
Uses the operator token, which the server accepts for reports too, so
the host that was collected needs no token of its own.`,
		Args: usageArgs(1, 1, "drivelist admin ingest FILE"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var data []byte
			var err error
			if args[0] == "-" {
				data, err = io.ReadAll(cmd.InOrStdin())
			} else {
				data, err = os.ReadFile(args[0])
			}
			if err != nil {
				return err
			}
			var bundle pb.ReportBundle
			if err := protojson.Unmarshal(data, &bundle); err != nil {
				return fmt.Errorf("not a report bundle: %w", err)
			}
			if bundle.Inventory == nil || bundle.Inventory.Host == nil {
				return errors.New("the bundle has no inventory report")
			}
			client, err := cfg.ingestClient()
			if err != nil {
				return err
			}
			res, err := client.ReportInventory(cmd.Context(), connect.NewRequest(bundle.Inventory))
			if err != nil {
				return rpcErr(err)
			}
			if !res.Msg.Accepted {
				return fmt.Errorf("server rejected the report from %s: %s", bundle.Inventory.Host.Hostname, res.Msg.RejectReason)
			}
			state := "no change"
			if res.Msg.Changed {
				state = "inventory changed"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ingested %d devices from %s: %s\n", len(bundle.Inventory.Devices), bundle.Inventory.Host.Hostname, state)
			if bundle.Smart != nil && len(bundle.Smart.Samples) > 0 {
				if _, err := client.ReportSmart(cmd.Context(), connect.NewRequest(bundle.Smart)); err != nil {
					return rpcErr(err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "ingested %d SMART samples\n", len(bundle.Smart.Samples))
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "rebuild",
		Short: "Recompute every placement and derived event from the stored snapshots",
		Long: `rebuild throws away the placements and the events derived from reports
(first seen, vanished, moved, use changed, and so on) and recomputes
them from the snapshots the server kept, through the same logic
ingest uses. Manual annotations, merges, samples, ghosts and host
events are kept. Use it after a fix to the ingest logic; it is also
the check that history is a pure function of the reports.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := cfg.queryClient()
			if err != nil {
				return err
			}
			res, err := client.Rebuild(cmd.Context(), connect.NewRequest(&pb.RebuildRequest{}))
			if err != nil {
				return rpcErr(err)
			}
			if cfg.json {
				return printJSON(cmd.OutOrStdout(), res.Msg)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "rebuilt from %d snapshots: %d placements, %d events\n", res.Msg.Snapshots, res.Msg.Placements, res.Msg.Events)
			return nil
		},
	})
	return cmd
}

func tab(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 8, 2, ' ', 0) }

// ---------- report ----------

func newReportCmd(cfg *clientConfig) *cobra.Command {
	var output string
	var withSmart bool
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Collect this host's inventory once and send it to the server, or write it to a file",
		Long: `report runs the collector and posts the result as one inventory report.
It is what the agent does on a timer; run it by hand or from cron to
try the server without the agent. Needs the agent token.

With --output, nothing is sent: the report is written as JSON (with a
SMART pass when --smart is given) for 'drivelist admin ingest' to
submit from a host that holds a token. That is how a host you would
rather not give a token to is still tracked:

  ssh web1 drivelist report --output - --smart | drivelist admin ingest -`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inv, collectErr := collectAll()
			topo, err := collectSAS()
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "drivelist: sas topology unreadable, reporting without it: %v\n", err)
			}
			req := report.FromInventory(report.Host(), inv, topo, time.Now(), collectErr)
			if output != "" {
				bundle := &pb.ReportBundle{Inventory: req}
				if withSmart {
					bundle.Smart = report.SmartPass(cmd.Context(), collect.DefaultSmartRunner, req.Host, inv, time.Now())
				}
				data, err := protojson.MarshalOptions{Multiline: true}.Marshal(bundle)
				if err != nil {
					return err
				}
				if output == "-" {
					_, err = cmd.OutOrStdout().Write(append(data, '\n'))
					return err
				}
				if err := os.WriteFile(output, append(data, '\n'), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "drivelist: wrote %d devices from %s to %s\n", len(req.Devices), req.Host.Hostname, output)
				if collectErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "drivelist: report is incomplete: %v\n", collectErr)
				}
				return nil
			}
			client, err := cfg.collectorClient()
			if err != nil {
				return err
			}
			res, err := client.ReportInventory(cmd.Context(), connect.NewRequest(req))
			if err != nil {
				return rpcErr(err)
			}
			if cfg.json {
				return printJSON(cmd.OutOrStdout(), res.Msg)
			}
			if !res.Msg.Accepted {
				return fmt.Errorf("server rejected the report: %s", res.Msg.RejectReason)
			}
			state := "no change"
			if res.Msg.Changed {
				state = "inventory changed"
			}
			flagged := 0
			for _, s := range res.Msg.Statuses {
				if s.Status != "ok" {
					flagged++
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "reported %d devices from %s: %s; %d flagged by the server\n", len(req.Devices), req.Host.Hostname, state, flagged)
			if collectErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "drivelist: report sent as incomplete: %v\n", collectErr)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "write the report here (- for stdout) instead of sending it")
	cmd.Flags().BoolVar(&withSmart, "smart", false, "with --output: include a SMART pass (needs root and smartctl)")
	return cmd
}

// ---------- hosts ----------

func newHostsCmd(cfg *clientConfig) *cobra.Command {
	var ids bool
	var to tableOpts
	cmd := &cobra.Command{
		Use:   "hosts",
		Short: "List the hosts that report to the server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := cfg.queryClient()
			if err != nil {
				return err
			}
			res, err := client.ListHosts(cmd.Context(), connect.NewRequest(&pb.ListHostsRequest{}))
			if err != nil {
				return rpcErr(err)
			}
			if cfg.json {
				return printJSON(cmd.OutOrStdout(), res.Msg)
			}
			if ids && !to.all && to.fields == "" {
				to.fields = "host,drives,missing,ghosts,last,state,agent,machineid"
			}
			return printTable(cmd.OutOrStdout(), to, hostCols, res.Msg.Hosts)
		},
	}
	cmd.Flags().BoolVar(&ids, "ids", false, "also show each host's machine id")
	addTableFlags(cmd, &to, hostCols)
	return cmd
}

// ---------- drives ----------

func newDrivesCmd(cfg *clientConfig) *cobra.Command {
	var (
		host, model    string
		status         []string
		unused, missng bool
		to             tableOpts
	)
	cmd := &cobra.Command{
		Use:   "drives",
		Short: "List drives across the fleet",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := cfg.queryClient()
			if err != nil {
				return err
			}
			res, err := client.ListDrives(cmd.Context(), connect.NewRequest(&pb.ListDrivesRequest{
				Host: host, Status: status, UnusedOnly: unused, MissingOnly: missng, Model: model,
			}))
			if err != nil {
				return rpcErr(err)
			}
			if cfg.json {
				return printJSON(cmd.OutOrStdout(), res.Msg)
			}
			return printTable(cmd.OutOrStdout(), to, driveCols, res.Msg.Drives)
		},
	}
	addTableFlags(cmd, &to, driveCols)
	f := cmd.Flags()
	f.StringVar(&host, "host", "", "only drives currently on this host")
	f.StringSliceVar(&status, "status", nil, "only these statuses (ok, suspect, bad, shelved, retired)")
	f.BoolVar(&unused, "unused", false, "only drives with no use")
	f.BoolVar(&missng, "missing", false, "only drives that vanished and are not expected to be absent")
	f.StringVar(&model, "model", "", "only models containing this text")
	return cmd
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ---------- drive ----------

func newDriveCmd(cfg *clientConfig) *cobra.Command {
	var note, since string
	var raw bool
	cmd := &cobra.Command{
		Use:   "drive REF [history | mark STATUS | note TEXT]",
		Short: "Show one drive, its history, or record a status or note on it",
		Long: `drive shows one drive. REF is a serial number, a WWN, or an unambiguous
prefix of either; an ambiguous prefix lists the candidates.

  drivelist drive REF              summary: identity, status, where it is now
  drivelist drive REF history      everything that has happened to it, oldest first
  drivelist drive REF kernel       kernel log error counts by hour (--since 7d)
  drivelist drive REF smart        SMART samples, newest first (--since 30d, --raw for the latest smartctl JSON)
  drivelist drive REF io           hourly I/O buckets, newest first (--since 7d)
  drivelist drive REF merge OTHER  fold OTHER's record into REF: one drive that got two records
  drivelist drive REF mark STATUS  set the status: ok, suspect, bad, shelved, retired
  drivelist drive REF note TEXT    record a note without changing the status`,
		Args: usageArgs(1, -1, "drivelist drive REF [history | kernel | smart | io | mark STATUS | note TEXT | merge OTHER]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, rest := args[0], args[1:]
			if len(rest) == 0 {
				return showDrive(cmd, cfg, ref)
			}
			switch rest[0] {
			case "history":
				if len(rest) != 1 {
					return fmt.Errorf("usage: drivelist drive REF history")
				}
				return showHistory(cmd, cfg, ref)
			case "kernel":
				if len(rest) != 1 {
					return fmt.Errorf("usage: drivelist drive REF kernel [--since 7d]")
				}
				if since == "" {
					since = "168h"
				}
				return showKernel(cmd, cfg, ref, since)
			case "smart":
				if len(rest) != 1 {
					return fmt.Errorf("usage: drivelist drive REF smart [--since 30d] [--raw]")
				}
				return showSmart(cmd, cfg, ref, since, raw)
			case "io":
				if len(rest) != 1 {
					return fmt.Errorf("usage: drivelist drive REF io [--since 7d]")
				}
				if since == "" {
					since = "168h"
				}
				return showIO(cmd, cfg, ref, since)
			case "mark":
				if len(rest) != 2 {
					return fmt.Errorf("usage: drivelist drive REF mark STATUS [--note TEXT]")
				}
				return annotate(cmd, cfg, ref, rest[1], note)
			case "note":
				if len(rest) < 2 {
					return fmt.Errorf("usage: drivelist drive REF note TEXT")
				}
				return annotate(cmd, cfg, ref, "", strings.Join(rest[1:], " "))
			case "merge":
				if len(rest) != 2 {
					return fmt.Errorf("usage: drivelist drive REF merge OTHER")
				}
				return mergeDrives(cmd, cfg, ref, rest[1])
			}
			return fmt.Errorf("unknown action %q: want history, kernel, smart, io, mark, note, or merge", rest[0])
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "with mark: why the status changed")
	cmd.Flags().StringVar(&since, "since", "", "with kernel or smart: how far back, as a duration (default 168h for kernel, 720h for smart)")
	cmd.Flags().BoolVar(&raw, "raw", false, "with smart: print the newest raw smartctl JSON instead of the table")
	return cmd
}

func mergeDrives(cmd *cobra.Command, cfg *clientConfig, into, from string) error {
	client, err := cfg.queryClient()
	if err != nil {
		return err
	}
	cfg.resolve()
	res, err := client.MergeDrives(cmd.Context(), connect.NewRequest(&pb.MergeDrivesRequest{Into: into, From: from, Actor: cfg.actor}))
	if err != nil {
		return rpcErr(err)
	}
	if cfg.json {
		return printJSON(cmd.OutOrStdout(), res.Msg)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s\n", res.Msg.Event.Serial, when(res.Msg.Event.Ts), describe(res.Msg.Event))
	return nil
}

func newHostCmd(cfg *clientConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "host merge INTO FROM",
		Short: "Fold one host record into another, after a reinstall gave it a new machine id",
		Long: `host merge moves everything recorded under host FROM (placements,
snapshots, events, samples) to host INTO and keeps FROM's machine id
resolving to INTO, so an agent still reporting under the old id lands
on the merged host. INTO and FROM are hostnames or, when two hosts
share a name, machine ids as 'hosts --ids' shows them.`,
		Args: usageArgs(3, 3, "drivelist host merge INTO FROM"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "merge" {
				return fmt.Errorf("usage: drivelist host merge INTO FROM")
			}
			client, err := cfg.queryClient()
			if err != nil {
				return err
			}
			cfg.resolve()
			res, err := client.MergeHosts(cmd.Context(), connect.NewRequest(&pb.MergeHostsRequest{Into: args[1], From: args[2], Actor: cfg.actor}))
			if err != nil {
				return rpcErr(err)
			}
			if cfg.json {
				return printJSON(cmd.OutOrStdout(), res.Msg)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n", when(res.Msg.Event.Ts), describe(res.Msg.Event))
			return nil
		},
	}
}

func showIO(cmd *cobra.Command, cfg *clientConfig, ref, since string) error {
	d, err := time.ParseDuration(since)
	if err != nil {
		return fmt.Errorf("--since: %w", err)
	}
	client, err := cfg.queryClient()
	if err != nil {
		return err
	}
	res, err := client.GetIO(cmd.Context(), connect.NewRequest(&pb.GetIORequest{Ref: ref, Since: timestamppb.New(time.Now().Add(-d))}))
	if err != nil {
		return rpcErr(err)
	}
	if cfg.json {
		return printJSON(cmd.OutOrStdout(), res.Msg)
	}
	w := cmd.OutOrStdout()
	printDriveHeader(w, res.Msg.Drive, nil)
	if len(res.Msg.Samples) == 0 {
		fmt.Fprintf(w, "no I/O samples in the last %s\n", since)
		return nil
	}
	fmt.Fprintln(w)
	tw := tab(w)
	fmt.Fprintln(tw, "START\tSPAN\tHOST\tREADS\tWRITES\tREAD\tWRITTEN\tR_AWAIT\tW_AWAIT\tUTIL\tR_MAX\tW_MAX\tUTIL_MAX\tDROPPED")
	for _, s := range res.Msg.Samples {
		span := time.Duration(s.BucketSecs) * time.Second
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", when(s.BucketStart), span, s.Hostname, s.Reads, s.Writes,
			size(s.ReadBytes), size(s.WriteBytes), ms(fdiv(s.ReadMs, s.AwaitReads)), ms(fdiv(s.WriteMs, s.AwaitWrites)), pct(fdiv(s.IoMs, uint64(s.BucketSecs)*1000)),
			ms(s.RAwaitMaxMs), ms(s.WAwaitMaxMs), pct(s.UtilMax), count(s.Glitches))
	}
	return tw.Flush()
}

func newIOCmd(cfg *clientConfig) *cobra.Command {
	var host, since string
	var to tableOpts
	cmd := &cobra.Command{
		Use:   "io [compare]",
		Short: "Compare each drive's latency and utilisation with its vdev's median",
		Long: `io compare lists every currently placed drive with its average read and
write latency and utilisation over the window, next to the median of
the vdev it belongs to, worst first within each vdev. A drive whose
latency is several times its siblings' is the one to look at.`,
		Args: usageArgs(0, 1, "drivelist io [compare] [--host H] [--since 24h]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && args[0] != "compare" {
				return fmt.Errorf("unknown action %q: compare is the only one; usage: drivelist io [compare] [--host H] [--since 24h]", args[0])
			}
			d, err := time.ParseDuration(since)
			if err != nil {
				return fmt.Errorf("--since: %w", err)
			}
			client, err := cfg.queryClient()
			if err != nil {
				return err
			}
			res, err := client.CompareIO(cmd.Context(), connect.NewRequest(&pb.CompareIORequest{Host: host, Since: timestamppb.New(time.Now().Add(-d))}))
			if err != nil {
				return rpcErr(err)
			}
			if cfg.json {
				return printJSON(cmd.OutOrStdout(), res.Msg)
			}
			if len(res.Msg.Rows) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no I/O samples in the last %s\n", since)
				return nil
			}
			return printTable(cmd.OutOrStdout(), to, ioCols, res.Msg.Rows)
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "only drives on this host")
	cmd.Flags().StringVar(&since, "since", "24h", "window, as a duration")
	addTableFlags(cmd, &to, ioCols)
	return cmd
}

func showSmart(cmd *cobra.Command, cfg *clientConfig, ref, since string, raw bool) error {
	if since == "" {
		since = "720h"
	}
	d, err := time.ParseDuration(since)
	if err != nil {
		return fmt.Errorf("--since: %w", err)
	}
	client, err := cfg.queryClient()
	if err != nil {
		return err
	}
	res, err := client.GetSmart(cmd.Context(), connect.NewRequest(&pb.GetSmartRequest{Ref: ref, Since: timestamppb.New(time.Now().Add(-d)), IncludeRaw: raw}))
	if err != nil {
		return rpcErr(err)
	}
	if cfg.json {
		return printJSON(cmd.OutOrStdout(), res.Msg)
	}
	w := cmd.OutOrStdout()
	if raw {
		if len(res.Msg.RawJson) == 0 {
			return fmt.Errorf("no raw smartctl output stored for this drive")
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "smartctl output from %s\n", when(res.Msg.RawTs))
		_, err := w.Write(append(res.Msg.RawJson, '\n'))
		return err
	}
	printDriveHeader(w, res.Msg.Drive, nil)
	if len(res.Msg.Samples) == 0 {
		fmt.Fprintf(w, "no SMART samples in the last %s\n", since)
		return nil
	}
	fmt.Fprintln(w)
	tw := tab(w)
	fmt.Fprintln(tw, "TIME\tHOST\tHEALTH\tHOURS\tTEMP\tREALLOC\tPENDING\tUNCORR\tCRC\tREAD\tWRITTEN\tWEAR\tSELF-TEST")
	for _, s := range res.Msg.Samples {
		if s.Skipped != "" {
			fmt.Fprintf(tw, "%s\t%s\tskipped: %s\t\t\t\t\t\t\t\t\t\t\n", when(s.Ts), s.Hostname, s.Skipped)
			continue
		}
		m := s.Summary
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", when(s.Ts), s.Hostname,
			health(m), optU(m.PowerOnHours), optTemp(m.TempC), optU(m.Reallocated), optU(m.Pending), optU(m.Uncorrectable), optU(m.CrcErrors),
			optBytes(m.ReadBytes), optBytes(m.WriteBytes), optPct(m.PercentUsed), orDash(m.SelftestLast))
	}
	return tw.Flush()
}

func showKernel(cmd *cobra.Command, cfg *clientConfig, ref, since string) error {
	d, err := time.ParseDuration(since)
	if err != nil {
		return fmt.Errorf("--since: %w", err)
	}
	client, err := cfg.queryClient()
	if err != nil {
		return err
	}
	res, err := client.GetKernel(cmd.Context(), connect.NewRequest(&pb.GetKernelRequest{Ref: ref, Since: timestamppb.New(time.Now().Add(-d))}))
	if err != nil {
		return rpcErr(err)
	}
	if cfg.json {
		return printJSON(cmd.OutOrStdout(), res.Msg)
	}
	w := cmd.OutOrStdout()
	printDriveHeader(w, res.Msg.Drive, nil)
	if len(res.Msg.Samples) == 0 {
		fmt.Fprintf(w, "no kernel log lines in the last %s\n", since)
		return nil
	}
	fmt.Fprintln(w)
	tw := tab(w)
	fmt.Fprintln(tw, "HOUR\tHOST\tDEVICE\tCLASS\tCODE\tCOUNT\tSAMPLE")
	for _, k := range res.Msg.Samples {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", when(k.BucketStart), k.Hostname, k.DevName, k.Class, orDash(k.ScsiCode), k.Count, k.Sample)
	}
	return tw.Flush()
}

func showDrive(cmd *cobra.Command, cfg *clientConfig, ref string) error {
	client, err := cfg.queryClient()
	if err != nil {
		return err
	}
	res, err := client.GetDrive(cmd.Context(), connect.NewRequest(&pb.GetDriveRequest{Ref: ref}))
	if err != nil {
		return rpcErr(err)
	}
	if cfg.json {
		return printJSON(cmd.OutOrStdout(), res.Msg)
	}
	w := cmd.OutOrStdout()
	printDriveHeader(w, res.Msg.Drive, res.Msg.LastStatus)
	if len(res.Msg.Keys) > 0 {
		fmt.Fprintf(w, "keys: %s\n", strings.Join(res.Msg.Keys, ", "))
	}
	return nil
}

func showHistory(cmd *cobra.Command, cfg *clientConfig, ref string) error {
	client, err := cfg.queryClient()
	if err != nil {
		return err
	}
	res, err := client.GetDriveHistory(cmd.Context(), connect.NewRequest(&pb.GetDriveHistoryRequest{Ref: ref}))
	if err != nil {
		return rpcErr(err)
	}
	if cfg.json {
		return printJSON(cmd.OutOrStdout(), res.Msg)
	}
	w := cmd.OutOrStdout()
	printDriveHeader(w, res.Msg.Drive, nil)
	fmt.Fprintln(w)
	for _, e := range res.Msg.Events {
		fmt.Fprintf(w, "%s  %s\n", when(e.Ts), describe(e))
	}
	if p := res.Msg.Drive.Current; p != nil {
		fmt.Fprintf(w, "\nstill present on %s %s  %s  last confirmed %s\n", p.Hostname, slotOf(p), useSummary(p.Uses), when(p.LastSeen))
	}
	return nil
}

func annotate(cmd *cobra.Command, cfg *clientConfig, ref, status, note string) error {
	client, err := cfg.queryClient()
	if err != nil {
		return err
	}
	cfg.resolve()
	res, err := client.Annotate(cmd.Context(), connect.NewRequest(&pb.AnnotateRequest{Ref: ref, Status: status, Note: note, Actor: cfg.actor}))
	if err != nil {
		return rpcErr(err)
	}
	if cfg.json {
		return printJSON(cmd.OutOrStdout(), res.Msg)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s\n", res.Msg.Event.Serial, when(res.Msg.Event.Ts), describe(res.Msg.Event))
	return nil
}

func printDriveHeader(w io.Writer, d *pb.Drive, last *pb.Event) {
	fmt.Fprintf(w, "%s %s  serial %s  wwn %s  %s  %s\n", d.Vendor, d.Model, d.Serial, orDash(d.Wwn), size(d.SizeBytes), busShort[d.Bus])
	status := "status: " + d.Status
	if last != nil {
		det := detailOf(last)
		status += fmt.Sprintf(" (%s, %s: %q)", strings.TrimPrefix(last.Source, "user:"), when(last.Ts), str(det, "note"))
	}
	fmt.Fprintln(w, status)
	switch {
	case d.Current != nil:
		p := d.Current
		fmt.Fprintf(w, "now: %s  %s  %s  %s  since %s, confirmed %s\n", p.Hostname, slotOf(p), orDash(p.DevName), useSummary(p.Uses), when(p.FirstSeen), when(p.LastSeen))
		if d.MemberState != "" {
			fmt.Fprintf(w, "zfs: %s\n", d.MemberState)
		}
	case d.Last != nil:
		p := d.Last
		fmt.Fprintf(w, "not present; last %s  %s  %s  %s  %s to %s (%s)\n", p.Hostname, slotOf(p), orDash(p.DevName), useSummary(p.Uses), when(p.FirstSeen), when(p.EndedAt), p.EndReason)
	}
}

// ---------- events ----------

func newEventsCmd(cfg *clientConfig) *cobra.Command {
	var (
		since, host string
		kinds       []string
		limit       int32
	)
	cmd := &cobra.Command{
		Use:   "events",
		Short: "List recent events across the fleet, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := cfg.queryClient()
			if err != nil {
				return err
			}
			req := &pb.ListEventsRequest{Kinds: kinds, Host: host, Limit: limit}
			if since != "" {
				d, err := time.ParseDuration(since)
				if err != nil {
					return fmt.Errorf("--since: %w (use a duration such as 24h or 7d written as 168h)", err)
				}
				req.Since = timestamppb.New(time.Now().Add(-d))
			}
			res, err := client.ListEvents(cmd.Context(), connect.NewRequest(req))
			if err != nil {
				return rpcErr(err)
			}
			if cfg.json {
				return printJSON(cmd.OutOrStdout(), res.Msg)
			}
			w := tab(cmd.OutOrStdout())
			fmt.Fprintln(w, "TIME\tDRIVE\tEVENT")
			for _, e := range res.Msg.Events {
				fmt.Fprintf(w, "%s\t%s\t%s\n", when(e.Ts), orDash(e.Serial), describe(e))
			}
			return w.Flush()
		},
	}
	f := cmd.Flags()
	f.StringVar(&since, "since", "", "only events in the last duration, e.g. 24h")
	f.StringSliceVar(&kinds, "kind", nil, "only these kinds, e.g. vanished,moved_host")
	f.StringVar(&host, "host", "", "only events on this host")
	f.Int32Var(&limit, "limit", 0, "at most this many (server default 200)")
	return cmd
}

// ---------- smart ----------

func newSmartCmd(cfg *clientConfig) *cobra.Command {
	var host string
	var problems bool
	var to tableOpts
	cmd := &cobra.Command{
		Use:   "smart",
		Short: "Every drive's newest SMART reading, problems first",
		Long: `smart lists every placed drive with its newest SMART reading: health,
hours, temperature, the error counters, wear, and the last self-test,
with how old the reading is. Drives whose reading says something is
wrong (health failed, any error counter nonzero, 80% of rated life
used) come first; --problems shows only those. A drive whose newest
sample was skipped (standby, unsupported) shows why beside its last
real reading. For one drive's history, use 'drive REF smart'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := cfg.queryClient()
			if err != nil {
				return err
			}
			res, err := client.ListSmart(cmd.Context(), connect.NewRequest(&pb.ListSmartRequest{Host: host, Problems: problems}))
			if err != nil {
				return rpcErr(err)
			}
			if cfg.json {
				return printJSON(cmd.OutOrStdout(), res.Msg)
			}
			if len(res.Msg.Rows) == 0 {
				if problems {
					fmt.Fprintln(cmd.OutOrStdout(), "no drive's SMART reading reports a problem")
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "no placed drives")
				}
				return nil
			}
			return printTable(cmd.OutOrStdout(), to, smartCols, res.Msg.Rows)
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "only drives on this host")
	cmd.Flags().BoolVar(&problems, "problems", false, "only drives whose reading says something is wrong")
	addTableFlags(cmd, &to, smartCols)
	return cmd
}

// ---------- enclosures ----------

func newEnclosuresCmd(cfg *clientConfig) *cobra.Command {
	var to tableOpts
	cmd := &cobra.Command{
		Use:   "enclosures",
		Short: "List the enclosures (shelves, backplanes, front panels) drives sit in",
		Long: `enclosures lists every enclosure a host has reported: the host, the
node that reaches it (an expander, or the HBA for its own bays), that
node's model, the name a person gave it with 'enclosure KEY name', the
drives in it and the bays seen, and the key placements use (the SES
enclosure identifier).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := cfg.queryClient()
			if err != nil {
				return err
			}
			res, err := client.ListEnclosures(cmd.Context(), connect.NewRequest(&pb.ListEnclosuresRequest{}))
			if err != nil {
				return rpcErr(err)
			}
			if cfg.json {
				return printJSON(cmd.OutOrStdout(), res.Msg)
			}
			return printTable(cmd.OutOrStdout(), to, enclosureCols, res.Msg.Enclosures)
		},
	}
	addTableFlags(cmd, &to, enclosureCols)
	return cmd
}

func newEnclosureCmd(cfg *clientConfig) *cobra.Command {
	var note string
	cmd := &cobra.Command{
		Use:   "enclosure KEY [name NAME | bays]",
		Short: "Name an enclosure, or show it bay by bay",
		Long: `enclosure KEY name NAME records what you call an enclosure: "front
shelf", "JBOD 2", "fs2-front". Every listing shows the name in place of
the kernel's expander-H:N or hostH from then on. An empty NAME clears
it. enclosure KEY bays shows the enclosure bay by bay as its hardware
profile lays them out, with what sits in each, then any occupied bay
the profile does not know. KEY is the key 'enclosures' prints, an
unambiguous part of it, the name, or the reaching node's kernel name
if only one host has one so named.`,
		Args: usageArgs(2, -1, "drivelist enclosure KEY name NAME [--note TEXT] | drivelist enclosure KEY bays"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[1] == "bays" {
				return showBays(cmd, cfg, args[0])
			}
			if args[1] != "name" {
				return fmt.Errorf("usage: drivelist enclosure KEY name NAME [--note TEXT] | drivelist enclosure KEY bays")
			}
			client, err := cfg.queryClient()
			if err != nil {
				return err
			}
			cfg.resolve()
			res, err := client.NameEnclosure(cmd.Context(), connect.NewRequest(&pb.NameEnclosureRequest{Ref: args[0], Name: strings.Join(args[2:], " "), Note: note, Actor: cfg.actor}))
			if err != nil {
				return rpcErr(err)
			}
			if cfg.json {
				return printJSON(cmd.OutOrStdout(), res.Msg)
			}
			e := res.Msg.Enclosure
			if e.Name == "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: name cleared\n", e.Enclosure)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "%s (%s on %s): named %q\n", e.Enclosure, orDash(e.Via), orDash(e.Hostname), e.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "free text kept with the name: where it is, what is in it")
	return cmd
}

func showBays(cmd *cobra.Command, cfg *clientConfig, ref string) error {
	client, err := cfg.queryClient()
	if err != nil {
		return err
	}
	res, err := client.ListBays(cmd.Context(), connect.NewRequest(&pb.ListBaysRequest{Ref: ref}))
	if err != nil {
		return rpcErr(err)
	}
	if cfg.json {
		return printJSON(cmd.OutOrStdout(), res.Msg)
	}
	w := cmd.OutOrStdout()
	e := res.Msg.Enclosure
	fmt.Fprintf(w, "%s  %s  %s on %s", orDash(e.Name), orDash(e.Product), orDash(e.Via), orDash(e.Hostname))
	if e.Profile != "" {
		fmt.Fprintf(w, "  profile: %s", e.Profile)
	} else {
		fmt.Fprint(w, "  no hardware profile: bays are as the firmware names them")
	}
	fmt.Fprintln(w)
	if res.Msg.Rows > 0 {
		fmt.Fprintln(w)
		printBayGrid(w, res.Msg)
	}
	fmt.Fprintln(w)
	tw := tab(w)
	fmt.Fprintln(tw, "BAY\tIDS\tDEVICE\tSERIAL\tMODEL\tSTATUS\tUSES")
	for _, b := range res.Msg.Bays {
		label := b.Label
		if !b.Declared {
			label += " (not in profile)"
		}
		if !b.Present {
			fmt.Fprintf(tw, "%s\t%s\t-\t-\t-\t-\t%s\n", label, orDash(strings.Join(b.Ids, " ")), "empty")
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", label, orDash(strings.Join(b.Ids, " ")), b.DevName, b.Serial, b.Model, b.Status, useSummary(b.Uses))
	}
	return tw.Flush()
}

// printBayGrid draws the profile's layout: one cell per bay, the bay's
// name and what is in it, declared bays only.
func printBayGrid(w io.Writer, res *pb.ListBaysResponse) {
	rows, cols := int(res.Rows), int(res.Columns)
	cells := make([][]string, rows)
	for r := range cells {
		cells[r] = make([]string, cols)
		for c := range cells[r] {
			cells[r][c] = ""
		}
	}
	i := 0
	for _, b := range res.Bays {
		if !b.Declared {
			continue
		}
		var r, c int
		if res.Order == "row-major" {
			r, c = i/cols, i%cols
		} else {
			r, c = i%rows, i/rows
		}
		i++
		if r >= rows || c >= cols {
			continue
		}
		cell := b.Label
		if b.Present {
			cell += ":" + b.DevName
		} else {
			cell += ":-"
		}
		cells[r][c] = cell
	}
	width := 0
	for _, row := range cells {
		for _, cell := range row {
			width = max(width, len(cell))
		}
	}
	for _, row := range cells {
		for c, cell := range row {
			if c > 0 {
				fmt.Fprint(w, "  ")
			}
			fmt.Fprintf(w, "[%-*s]", width, cell)
		}
		fmt.Fprintln(w)
	}
}

// ---------- hardware ----------

func newHardwareCmd(cfg *clientConfig) *cobra.Command {
	var dir string
	var to tableOpts
	cmd := &cobra.Command{
		Use:   "hardware [check HOST]",
		Short: "List the hardware profiles built in, or check a host's enclosures against them",
		Long: `hardware lists the enclosure models drivelist has a profile for: what
each bay is called and which firmware identities land in it. hardware
check HOST asks the server which of HOST's enclosures matched a
profile and which occupied bays no profile names, which is what a
profile for a new box needs. --dir adds profiles from a directory,
overriding built-in ones for the same model, to try one before
contributing it (the server takes the same with serve --hardware-dir).`,
		Args: usageArgs(0, 2, "drivelist hardware [check HOST]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			hw, err := hardware.Load(dir)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return printTable(cmd.OutOrStdout(), to, profileCols, hw.All())
			}
			if args[0] != "check" || len(args) != 2 {
				return fmt.Errorf("usage: drivelist hardware [check HOST]")
			}
			return hardwareCheck(cmd, cfg, args[1])
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "directory of extra hardware profiles")
	addTableFlags(cmd, &to, profileCols)
	return cmd
}

func hardwareCheck(cmd *cobra.Command, cfg *clientConfig, host string) error {
	client, err := cfg.queryClient()
	if err != nil {
		return err
	}
	res, err := client.ListEnclosures(cmd.Context(), connect.NewRequest(&pb.ListEnclosuresRequest{}))
	if err != nil {
		return rpcErr(err)
	}
	w := cmd.OutOrStdout()
	found := false
	for _, e := range res.Msg.Enclosures {
		if e.Hostname != host {
			continue
		}
		found = true
		fmt.Fprintf(w, "%s  %s  %s  %s\n", e.Enclosure, orDash(e.Name), orDash(e.Via), orDash(e.Product))
		if e.Profile == "" {
			fmt.Fprintf(w, "  no profile for model %q", e.Product)
			if e.Board != "" {
				fmt.Fprintf(w, " (board %q)", e.Board)
			}
			fmt.Fprintln(w)
		} else {
			fmt.Fprintf(w, "  profile: %s\n", e.Profile)
		}
		bays, err := client.ListBays(cmd.Context(), connect.NewRequest(&pb.ListBaysRequest{Ref: e.Enclosure}))
		if err != nil {
			return rpcErr(err)
		}
		for _, b := range bays.Msg.Bays {
			if !b.Declared {
				fmt.Fprintf(w, "  %s holds %s %s: no bay in the profile\n", strings.Join(b.Ids, " "), b.DevName, b.Serial)
			}
		}
	}
	if !found {
		return fmt.Errorf("host %q has no enclosures", host)
	}
	return nil
}

// ---------- missing ----------

func newMissingCmd(cfg *clientConfig) *cobra.Command {
	var to tableOpts
	cmd := &cobra.Command{
		Use:   "missing",
		Short: "Drives that vanished and are not marked bad, shelved or retired, plus pool ghosts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := cfg.queryClient()
			if err != nil {
				return err
			}
			res, err := client.ListMissing(cmd.Context(), connect.NewRequest(&pb.ListMissingRequest{}))
			if err != nil {
				return rpcErr(err)
			}
			if cfg.json {
				return printJSON(cmd.OutOrStdout(), res.Msg)
			}
			out := cmd.OutOrStdout()
			var gone []*pb.Drive
			for _, d := range res.Msg.Drives {
				if d.Last != nil {
					gone = append(gone, d)
				}
			}
			if err := printTable(out, to, missingCols, gone); err != nil {
				return err
			}
			if len(res.Msg.Ghosts) > 0 {
				fmt.Fprintln(out)
				fmt.Fprintln(out, "Pool members with no present device:")
				w := tab(out)
				fmt.Fprintln(w, "HOST\tPOOL\tMEMBER\tSTATE\tDRIVE\tSINCE")
				for _, g := range res.Msg.Ghosts {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", g.Hostname, g.Pool, g.Path, g.State, orDash(g.Serial), when(g.FirstSeen))
				}
				w.Flush()
			}
			return nil
		},
	}
	addTableFlags(cmd, &to, missingCols)
	return cmd
}
