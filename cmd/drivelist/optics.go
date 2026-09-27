package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/scottlaird/drivelist/collect"
	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
	"github.com/scottlaird/drivelist/internal/report"
	"github.com/scottlaird/drivelist/internal/store"
)

// ---------- formatting ----------

// dbm formats milliwatts as dBm to one decimal; "-" when unset or zero.
func dbm(mw *float64) string {
	if d := collect.DBm(mw); d != nil {
		return strconv.FormatFloat(*d, 'f', 1, 64)
	}
	return "-"
}

// worstLane is the lowest reading of one quantity across lanes, in dBm,
// with its lane when the module has several: "-13.0 L3".
func worstLane(o *pb.Optic, rx bool) (string, float64, bool) {
	best, lane, found := 0.0, uint32(0), false
	for _, l := range o.Lanes {
		v := l.TxMw
		if rx {
			v = l.RxMw
		}
		if v == nil {
			continue
		}
		if !found || *v < best {
			best, lane, found = *v, l.Lane, true
		}
	}
	if !found {
		return "-", 0, false
	}
	text := dbm(&best)
	if len(o.Lanes) > 1 {
		text += " L" + strconv.Itoa(int(lane))
	}
	return text, best, true
}

func laneList(o *pb.Optic, pick func(*pb.OpticLane) *float64, format func(*float64) string) string {
	var parts []string
	for _, l := range o.Lanes {
		parts = append(parts, format(pick(l)))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func tempText(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f°C", *v)
}

func optNum(v *float64, format string) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf(format, *v)
}

// parseSince reads a duration that may be given in days: "7d", "36h".
func parseSince(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, fmt.Errorf("--since %q: %w", s, err)
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("--since: %w", err)
	}
	return d, nil
}

var opticCols = []col[*pb.OpticRow]{
	{name: "host", header: "HOST", value: func(r *pb.OpticRow) string {
		if !r.Present {
			return "(" + orDash(r.Hostname) + ")"
		}
		return r.Hostname
	}},
	{name: "port", header: "PORT", value: func(r *pb.OpticRow) string {
		if n := len(r.Optic.Ports); n > 1 {
			return fmt.Sprintf("%s +%d", r.Optic.Port, n-1)
		}
		return orDash(r.Optic.Port)
	}, key: func(r *pb.OpticRow) any { return r.Hostname + "\x00" + naturalKey(r.Optic.Port) }},
	{name: "form", header: "FORM", value: func(r *pb.OpticRow) string { return orDash(r.Optic.Form) }},
	{name: "kind", header: "KIND", value: func(r *pb.OpticRow) string { return orDash(r.Optic.Kind) }},
	{name: "part", header: "PART", value: func(r *pb.OpticRow) string { return orDash(r.Optic.Part) }},
	{name: "serial", header: "SERIAL", value: func(r *pb.OpticRow) string { return orDash(r.Optic.Serial) }},
	{name: "temp", header: "TEMP", value: func(r *pb.OpticRow) string { return tempText(r.Optic.TempC) }, key: func(r *pb.OpticRow) any { return optFloatKey(r.Optic.TempC) }},
	{name: "rx", header: "RX dBm", value: func(r *pb.OpticRow) string { t, _, _ := worstLane(r.Optic, true); return t }, key: func(r *pb.OpticRow) any {
		if _, v, ok := worstLane(r.Optic, true); ok {
			return v
		}
		return nil
	}},
	{name: "tx", header: "TX dBm", value: func(r *pb.OpticRow) string { t, _, _ := worstLane(r.Optic, false); return t }, key: func(r *pb.OpticRow) any {
		if _, v, ok := worstLane(r.Optic, false); ok {
			return v
		}
		return nil
	}},
	{name: "link", header: "LINK", value: func(r *pb.OpticRow) string { return orDash(r.Optic.Link) }},
	{name: "flaps", header: "FLAPS 24H", value: func(r *pb.OpticRow) string {
		if !r.Present {
			return "-"
		}
		return strconv.Itoa(int(r.Optic.FlapsDay))
	}, key: func(r *pb.OpticRow) any { return r.Optic.FlapsDay }},
	{name: "status", header: "STATUS", value: func(r *pb.OpticRow) string { return r.Status }},
	{name: "problems", header: "PROBLEMS", value: func(r *pb.OpticRow) string {
		if len(r.Problems) == 0 {
			return "-"
		}
		return strings.Join(r.Problems, "; ")
	}},
	{name: "vendor", header: "VENDOR", value: func(r *pb.OpticRow) string { return orDash(r.Optic.Vendor) }, extra: true},
	{name: "compliance", header: "TYPE", value: func(r *pb.OpticRow) string { return orDash(r.Optic.Compliance) }, extra: true},
	{name: "wavelength", header: "NM", value: func(r *pb.OpticRow) string {
		if r.Optic.WavelengthNm == 0 {
			return "-"
		}
		return strconv.FormatFloat(r.Optic.WavelengthNm, 'f', -1, 64)
	}, key: func(r *pb.OpticRow) any { return r.Optic.WavelengthNm }, extra: true},
	{name: "lanes", header: "LANES", value: func(r *pb.OpticRow) string { return strconv.Itoa(len(r.Optic.Lanes)) }, key: func(r *pb.OpticRow) any { return len(r.Optic.Lanes) }, extra: true},
	{name: "bias", header: "BIAS mA", value: func(r *pb.OpticRow) string {
		return laneList(r.Optic, func(l *pb.OpticLane) *float64 { return l.BiasMa }, func(v *float64) string { return optNum(v, "%.1f") })
	}, extra: true},
	{name: "voltage", header: "VOLTS", value: func(r *pb.OpticRow) string { return optNum(r.Optic.VoltageV, "%.2f") }, key: func(r *pb.OpticRow) any { return optFloatKey(r.Optic.VoltageV) }, extra: true},
	{name: "date", header: "DATE", value: func(r *pb.OpticRow) string { return orDash(r.Optic.DateCode) }, extra: true},
	{name: "ports", header: "PORTS", value: func(r *pb.OpticRow) string { return orDash(strings.Join(r.Optic.Ports, " ")) }, extra: true},
	{name: "since", header: "FIRST SEEN", value: func(r *pb.OpticRow) string { return when(r.FirstSeen) }, key: func(r *pb.OpticRow) any { return tsKey(r.FirstSeen) }, extra: true},
	{name: "sampled", header: "READ", value: func(r *pb.OpticRow) string {
		if r.SampledAt == nil {
			return "-"
		}
		return ago(r.SampledAt, time.Now())
	}, key: func(r *pb.OpticRow) any { return tsKey(r.SampledAt) }, extra: true},
}

// naturalKey pads the digit runs of a port name so that sorting the key
// as text puts swp2 before swp10.
func naturalKey(s string) string {
	var b strings.Builder
	digits := ""
	flush := func() {
		if digits != "" {
			b.WriteString(fmt.Sprintf("%08s", digits))
			digits = ""
		}
	}
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits += string(r)
			continue
		}
		flush()
		b.WriteRune(r)
	}
	flush()
	return b.String()
}

// ---------- optics ----------

func newOpticsCmd(cfg *clientConfig) *cobra.Command {
	var host string
	var problems, all, local bool
	var to tableOpts
	cmd := &cobra.Command{
		Use:   "optics",
		Short: "Every optic in the fleet's network ports, problems first",
		Long: `optics lists the pluggable modules in every network port the agents
report (SFP, QSFP, QSFP-DD, OSFP; optical transceivers, active optical
cables and copper DACs), with the latest reading: temperature, and the
lowest lane's received and transmitted power in dBm (with its lane on
multi-lane modules), and how often the port's link went down or up in
the last day (FLAPS 24H, from the kernel's carrier_changes count). An
optic has a problem when the module raised an alarm or warning flag
its reading bears out, when a reading is past one of the module's own
thresholds, when its link changed four or more times in any hour of
the last day (a failing optic often runs clean for hours between
bouts), or when it is marked suspect or bad; on a port whose link is
down, low light is expected and not counted. --all adds optics that
are in no port now, at their last. --local reads this host's ports
directly, without the server.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var rows []*pb.OpticRow
			if local {
				optics, err := collectOptics()
				if err != nil {
					return err
				}
				hostname, _ := os.Hostname()
				for _, o := range optics {
					row := &pb.OpticRow{Optic: report.Optic(o), Status: store.StatusOK, Hostname: hostname, Present: true}
					row.Problems, row.Dark = store.OpticProblems(localOptic(o), store.StatusOK, true)
					if problems && len(row.Problems) == 0 {
						continue
					}
					rows = append(rows, row)
				}
			} else {
				client, err := cfg.queryClient()
				if err != nil {
					return err
				}
				res, err := client.ListOptics(cmd.Context(), connect.NewRequest(&pb.ListOpticsRequest{Host: host, Problems: problems, All: all}))
				if err != nil {
					return rpcErr(err)
				}
				if cfg.json {
					return printJSON(cmd.OutOrStdout(), res.Msg)
				}
				rows = res.Msg.Rows
			}
			if len(rows) == 0 {
				if problems {
					fmt.Fprintln(cmd.OutOrStdout(), "no optic has a problem")
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "no optics reported")
				}
				return nil
			}
			return printTable(cmd.OutOrStdout(), to, opticCols, rows)
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "only optics on this host")
	cmd.Flags().BoolVar(&problems, "problems", false, "only optics with a problem")
	cmd.Flags().BoolVar(&all, "all", false, "include optics in no port now, at their last one")
	cmd.Flags().BoolVar(&local, "local", false, "read this host's ports directly instead of asking the server")
	addTableFlags(cmd, &to, opticCols)
	return cmd
}

// localOptic is a collected module in the store's terms, for the
// problem rule.
func localOptic(o collect.Optic) store.Optic {
	out := store.Optic{Port: o.Port, Ports: o.Ports, Link: o.Link, TempC: o.TempC, VoltageV: o.VoltageV, Thresholds: o.Thresholds, Flags: o.Flags}
	for _, l := range o.Lanes {
		out.Lanes = append(out.Lanes, store.OpticLane{Lane: l.Lane, BiasMA: l.BiasMA, TxMW: l.TxMW, RxMW: l.RxMW})
	}
	return out
}

// ---------- optic ----------

func newOpticCmd(cfg *clientConfig) *cobra.Command {
	var note, since string
	cmd := &cobra.Command{
		Use:   "optic REF [history | mark STATUS | note TEXT]",
		Short: "Show one optic, its readings, or record a status or note on it",
		Long: `optic shows one optic. REF is a serial number, an unambiguous prefix of
one, or HOST:PORT for the module in that port now.

  drivelist optic REF              identity, where it is, every lane, its thresholds, where it has been
  drivelist optic REF history      hourly readings and link flaps, newest first (--since 7d)
  drivelist optic REF mark STATUS  set the status: ok, suspect, bad, shelved, retired
  drivelist optic REF note TEXT    record a note without changing the status`,
		Args: usageArgs(1, -1, "drivelist optic REF [history | mark STATUS | note TEXT]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, rest := args[0], args[1:]
			if len(rest) == 0 {
				return showOptic(cmd, cfg, ref, false, "")
			}
			switch rest[0] {
			case "history":
				if len(rest) != 1 {
					return fmt.Errorf("usage: drivelist optic REF history [--since 7d]")
				}
				if since == "" {
					since = "7d"
				}
				return showOptic(cmd, cfg, ref, true, since)
			case "mark":
				if len(rest) != 2 {
					return fmt.Errorf("usage: drivelist optic REF mark STATUS [--note TEXT]")
				}
				return annotateOptic(cmd, cfg, ref, rest[1], note)
			case "note":
				if len(rest) < 2 {
					return fmt.Errorf("usage: drivelist optic REF note TEXT")
				}
				return annotateOptic(cmd, cfg, ref, "", strings.Join(rest[1:], " "))
			}
			return fmt.Errorf("unknown action %q: want history, mark, or note", rest[0])
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "with mark: why the status changed")
	cmd.Flags().StringVar(&since, "since", "", "with history: how far back, as a duration (7d, 36h)")
	return cmd
}

func annotateOptic(cmd *cobra.Command, cfg *clientConfig, ref, status, note string) error {
	client, err := cfg.queryClient()
	if err != nil {
		return err
	}
	cfg.resolve()
	res, err := client.AnnotateOptic(cmd.Context(), connect.NewRequest(&pb.AnnotateRequest{Ref: ref, Status: status, Note: note, Actor: cfg.actor}))
	if err != nil {
		return rpcErr(err)
	}
	if cfg.json {
		return printJSON(cmd.OutOrStdout(), res.Msg)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s\n", res.Msg.Event.OpticSerial, when(res.Msg.Event.Ts), describe(res.Msg.Event))
	return nil
}

func showOptic(cmd *cobra.Command, cfg *clientConfig, ref string, history bool, since string) error {
	req := &pb.GetOpticRequest{Ref: ref}
	if since != "" {
		d, err := parseSince(since)
		if err != nil {
			return err
		}
		req.Since = timestamppb.New(time.Now().Add(-d))
	}
	client, err := cfg.queryClient()
	if err != nil {
		return err
	}
	res, err := client.GetOptic(cmd.Context(), connect.NewRequest(req))
	if err != nil {
		return rpcErr(err)
	}
	if cfg.json {
		return printJSON(cmd.OutOrStdout(), res.Msg)
	}
	w := cmd.OutOrStdout()
	printOpticHeader(w, res.Msg.Row)
	if history {
		fmt.Fprintln(w)
		return printOpticSamples(w, res.Msg.Samples)
	}
	o := res.Msg.Row.Optic
	if len(o.Lanes) > 0 {
		fmt.Fprintln(w)
		tw := tab(w)
		fmt.Fprintln(tw, "LANE\tBIAS mA\tTX mW\tTX dBm\tRX mW\tRX dBm")
		for _, l := range o.Lanes {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", l.Lane, optNum(l.BiasMa, "%.2f"), optNum(l.TxMw, "%.4f"), dbm(l.TxMw), optNum(l.RxMw, "%.4f"), dbm(l.RxMw))
		}
		tw.Flush()
	}
	if t := thresholdText(o.Thresholds); t != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "thresholds (the module's own):")
		fmt.Fprint(w, t)
	}
	if len(res.Msg.Placements) > 0 {
		fmt.Fprintln(w)
		tw := tab(w)
		fmt.Fprintln(tw, "HOST\tPORT\tFROM\tUNTIL\tWHY")
		for _, p := range res.Msg.Placements {
			until, why := "now", "-"
			if p.EndedAt != nil {
				until, why = when(p.EndedAt), p.EndReason
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Hostname, p.Port, when(p.FirstSeen), until, why)
		}
		tw.Flush()
	}
	if len(res.Msg.Events) > 0 {
		fmt.Fprintln(w)
		n := min(len(res.Msg.Events), 10)
		for _, e := range res.Msg.Events[:n] {
			fmt.Fprintf(w, "%s  %s\n", when(e.Ts), describe(e))
		}
	}
	return nil
}

func printOpticHeader(w io.Writer, r *pb.OpticRow) {
	o := r.Optic
	var ident []string
	for _, s := range []string{o.Form, o.Kind, o.Compliance} {
		if s != "" {
			ident = append(ident, s)
		}
	}
	if o.WavelengthNm > 0 {
		ident = append(ident, strconv.FormatFloat(o.WavelengthNm, 'f', -1, 64)+"nm")
	}
	fmt.Fprintf(w, "%s %s  serial %s  rev %s  date %s  %s\n", o.Vendor, o.Part, orDash(o.Serial), orDash(o.Rev), orDash(o.DateCode), strings.Join(ident, ", "))
	fmt.Fprintf(w, "status: %s\n", r.Status)
	where := "not in any port; last "
	if r.Present {
		where = "now: "
	}
	line := where + r.Hostname + " " + o.Port
	if len(o.Ports) > 1 {
		line += " (" + strings.Join(o.Ports, " ") + ")"
	}
	line += "  link " + orDash(o.Link) + "  since " + when(r.FirstSeen)
	if r.SampledAt != nil {
		line += ", read " + ago(r.SampledAt, time.Now())
	}
	fmt.Fprintln(w, line)
	if o.TempC != nil || o.VoltageV != nil {
		fmt.Fprintf(w, "temp %s  voltage %s\n", tempText(o.TempC), optNum(o.VoltageV, "%.3f V"))
	}
	switch {
	case len(r.Problems) > 0:
		fmt.Fprintf(w, "problems: %s\n", strings.Join(r.Problems, "; "))
	case r.Dark:
		fmt.Fprintln(w, "link down: low light expected")
	}
}

// thresholdText lists the module's thresholds by quantity, power in dBm.
func thresholdText(th map[string]float64) string {
	if len(th) == 0 {
		return ""
	}
	order := []string{"temp", "voltage", "bias", "tx", "rx"}
	unit := map[string]string{"temp": "°C", "voltage": " V", "bias": " mA"}
	var b strings.Builder
	for _, q := range order {
		var parts []string
		for _, lvl := range []string{"low_alarm", "low_warning", "high_warning", "high_alarm"} {
			v, ok := th[q+"_"+lvl]
			if !ok {
				continue
			}
			text := strconv.FormatFloat(v, 'f', -1, 64) + unit[q]
			if q == "tx" || q == "rx" {
				text = dbm(&v) + " dBm"
			}
			parts = append(parts, strings.ReplaceAll(lvl, "_", " ")+" "+text)
		}
		if len(parts) > 0 {
			fmt.Fprintf(&b, "  %-8s %s\n", q, strings.Join(parts, ", "))
		}
	}
	return b.String()
}

func printOpticSamples(w io.Writer, samples []*pb.OpticSample) error {
	if len(samples) == 0 {
		fmt.Fprintln(w, "no readings in the window")
		return nil
	}
	tw := tab(w)
	fmt.Fprintln(tw, "HOUR\tHOST\tPORT\tLINK\tFLAPS\tTEMP\tRX dBm\tTX dBm\tFLAGS")
	for _, s := range samples {
		o := &pb.Optic{Lanes: s.Lanes}
		flags := "-"
		if len(s.Flags) > 0 {
			sorted := append([]string(nil), s.Flags...)
			sort.Strings(sorted)
			flags = strings.Join(sorted, "; ")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n", when(s.Ts), s.Hostname, s.Port, orDash(s.Link), s.Flaps, tempText(s.TempC),
			laneList(o, func(l *pb.OpticLane) *float64 { return l.RxMw }, dbm), laneList(o, func(l *pb.OpticLane) *float64 { return l.TxMw }, dbm), flags)
	}
	return tw.Flush()
}
