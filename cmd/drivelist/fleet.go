package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"

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
		newDriveCmd(cfg),
		newEventsCmd(cfg),
		newMissingCmd(cfg),
		newIOCmd(cfg),
	}
}

func tab(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 8, 2, ' ', 0) }

// ---------- report ----------

func newReportCmd(cfg *clientConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "report",
		Short: "Collect this host's inventory once and send it to the server",
		Long: `report runs the collector and posts the result as one inventory report.
It is what the agent does on a timer; run it by hand or from cron to
try the server without the agent. Needs the agent token.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := cfg.collectorClient()
			if err != nil {
				return err
			}
			inv, collectErr := collectAll()
			req := report.FromInventory(report.Host(), inv, time.Now(), collectErr)
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
}

// ---------- hosts ----------

func newHostsCmd(cfg *clientConfig) *cobra.Command {
	var ids bool
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
			now := time.Now()
			w := tab(cmd.OutOrStdout())
			fmt.Fprint(w, "HOST\tDRIVES\tMISSING\tGHOSTS\tLAST REPORT\tSTATE")
			if ids {
				fmt.Fprint(w, "\tMACHINE ID")
			}
			fmt.Fprintln(w)
			for _, h := range res.Msg.Hosts {
				state := "ok"
				if h.StaleSince != nil {
					state = "stale since " + when(h.StaleSince)
				}
				fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\t%s", h.Hostname, h.DriveCount, h.MissingCount, h.GhostCount, ago(h.LastReport, now), state)
				if ids {
					fmt.Fprintf(w, "\t%s", h.MachineId)
				}
				fmt.Fprintln(w)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&ids, "ids", false, "also show each host's machine id")
	return cmd
}

// ---------- drives ----------

func newDrivesCmd(cfg *clientConfig) *cobra.Command {
	var (
		host, model    string
		status         []string
		unused, missng bool
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
			w := tab(cmd.OutOrStdout())
			fmt.Fprintln(w, "HOST\tSLOT\tDEVICE\tMODEL\tSERIAL\tSIZE\tBUS\tSTATUS\tZFS\tUSES")
			for _, d := range res.Msg.Drives {
				p := d.Current
				hostName, dev := "-", "-"
				if p == nil {
					if d.Last != nil {
						p = d.Last
						hostName = "(" + p.Hostname + ")"
					}
				} else {
					hostName, dev = p.Hostname, p.DevName
				}
				sl, uses := "-", "-"
				if p != nil {
					sl = slot(p.Expander, p.Bay)
					uses = useSummary(p.Uses)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					hostName, sl, dev, d.Model, d.Serial, size(d.SizeBytes), busShort[d.Bus], d.Status, orDash(d.MemberState), uses)
			}
			return w.Flush()
		},
	}
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
  drivelist drive REF mark STATUS  set the status: ok, suspect, bad, shelved, retired
  drivelist drive REF note TEXT    record a note without changing the status`,
		Args: cobra.MinimumNArgs(1),
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
			}
			return fmt.Errorf("unknown action %q: want history, kernel, smart, io, mark, or note", rest[0])
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "with mark: why the status changed")
	cmd.Flags().StringVar(&since, "since", "", "with kernel or smart: how far back, as a duration (default 168h for kernel, 720h for smart)")
	cmd.Flags().BoolVar(&raw, "raw", false, "with smart: print the newest raw smartctl JSON instead of the table")
	return cmd
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
	fmt.Fprintln(tw, "START\tSPAN\tHOST\tREADS\tWRITES\tREAD\tWRITTEN\tR_AWAIT\tW_AWAIT\tUTIL\tR_MAX\tW_MAX\tUTIL_MAX")
	for _, s := range res.Msg.Samples {
		span := time.Duration(s.BucketSecs) * time.Second
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", when(s.BucketStart), span, s.Hostname, s.Reads, s.Writes,
			size(s.ReadBytes), size(s.WriteBytes), ms(fdiv(s.ReadMs, s.Reads)), ms(fdiv(s.WriteMs, s.Writes)), pct(fdiv(s.IoMs, uint64(s.BucketSecs)*1000)),
			ms(s.RAwaitMaxMs), ms(s.WAwaitMaxMs), pct(s.UtilMax))
	}
	return tw.Flush()
}

func newIOCmd(cfg *clientConfig) *cobra.Command {
	var host, since string
	cmd := &cobra.Command{
		Use:   "io compare",
		Short: "Compare each drive's latency and utilisation with its vdev's median",
		Long: `io compare lists every currently placed drive with its average read and
write latency and utilisation over the window, next to the median of
the vdev it belongs to, worst first within each vdev. A drive whose
latency is several times its siblings' is the one to look at.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "compare" {
				return fmt.Errorf("usage: drivelist io compare [--host H] [--since 24h]")
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
			tw := tab(cmd.OutOrStdout())
			fmt.Fprintln(tw, "VDEV\tHOST\tDEVICE\tSERIAL\tMODEL\tR_AWAIT\tvs MED\tW_AWAIT\tvs MED\tUTIL\tvs MED\tREADS\tWRITES")
			for _, r := range res.Msg.Rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\n", groupLabel(r.Group, int(r.GroupSize)), r.Hostname, r.DevName, r.Serial, r.Model,
					ms(r.RAwaitMs), times(r.RAwaitMs, r.GroupRAwaitMs), ms(r.WAwaitMs), times(r.WAwaitMs, r.GroupWAwaitMs), pct(r.Util), times(r.Util, r.GroupUtil), r.Reads, r.Writes)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "only drives on this host")
	cmd.Flags().StringVar(&since, "since", "24h", "window, as a duration")
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
		fmt.Fprintf(w, "%s  present       %s  %s  %s  last confirmed %s\n", when(p.LastSeen), p.Hostname, slot(p.Expander, p.Bay), useSummary(p.Uses), when(p.LastSeen))
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
		fmt.Fprintf(w, "now: %s  %s  %s  %s  since %s, confirmed %s\n", p.Hostname, slot(p.Expander, p.Bay), orDash(p.DevName), useSummary(p.Uses), when(p.FirstSeen), when(p.LastSeen))
		if d.MemberState != "" {
			fmt.Fprintf(w, "zfs: %s\n", d.MemberState)
		}
	case d.Last != nil:
		p := d.Last
		fmt.Fprintf(w, "not present; last %s  %s  %s  %s  %s to %s (%s)\n", p.Hostname, slot(p.Expander, p.Bay), orDash(p.DevName), useSummary(p.Uses), when(p.FirstSeen), when(p.EndedAt), p.EndReason)
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

// ---------- missing ----------

func newMissingCmd(cfg *clientConfig) *cobra.Command {
	return &cobra.Command{
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
			w := tab(out)
			fmt.Fprintln(w, "SERIAL\tMODEL\tSTATUS\tLAST HOST\tLAST SLOT\tLAST USES\tLAST CONFIRMED\tNOTICED GONE")
			for _, d := range res.Msg.Drives {
				p := d.Last
				if p == nil {
					continue
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.Serial, d.Model, d.Status, p.Hostname, slot(p.Expander, p.Bay), useSummary(p.Uses), when(p.LastSeen), when(p.EndedAt))
			}
			w.Flush()
			if len(res.Msg.Ghosts) > 0 {
				fmt.Fprintln(out)
				fmt.Fprintln(out, "Pool members with no present device:")
				w = tab(out)
				fmt.Fprintln(w, "HOST\tPOOL\tMEMBER\tSTATE\tDRIVE\tSINCE")
				for _, g := range res.Msg.Ghosts {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", g.Hostname, g.Pool, g.Path, g.State, orDash(g.Serial), when(g.FirstSeen))
				}
				w.Flush()
			}
			return nil
		},
	}
}
