package main

import (
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
)

// ---------- dimm ----------

func newDimmCmd(cfg *clientConfig) *cobra.Command {
	var note string
	cmd := &cobra.Command{
		Use:   "dimm REF [mark STATUS | note TEXT]",
		Short: "Show one memory module, or record a status or note on it",
		Long: `dimm shows one memory module. REF is a serial number, an unambiguous
prefix of one, PART/SERIAL, or HOST:SLOT for the module in that slot (the
slot as dimms prints it, or an unambiguous prefix).

  drivelist dimm REF              its status, the slots it has been in, its events
  drivelist dimm REF mark STATUS  set the status: ok, suspect, bad, shelved, retired
  drivelist dimm REF note TEXT    record a note without changing the status

A status belongs to the module, by part and serial, so it follows the
module to another slot or host. Modules whose firmware reports no serial
or a placeholder one (00000000, "Unknown") can't be told apart, so they
can't be marked.`,
		Args: usageArgs(1, -1, "drivelist dimm REF [mark STATUS | note TEXT]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, rest := args[0], args[1:]
			if len(rest) == 0 {
				return showDimm(cmd, cfg, ref)
			}
			switch rest[0] {
			case "mark":
				if len(rest) != 2 {
					return fmt.Errorf("usage: drivelist dimm REF mark STATUS [--note TEXT]")
				}
				return annotateDimm(cmd, cfg, ref, rest[1], note)
			case "note":
				if len(rest) < 2 {
					return fmt.Errorf("usage: drivelist dimm REF note TEXT")
				}
				return annotateDimm(cmd, cfg, ref, "", strings.Join(rest[1:], " "))
			}
			return fmt.Errorf("unknown action %q: want mark or note", rest[0])
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "with mark: why the status changed")
	return cmd
}

func annotateDimm(cmd *cobra.Command, cfg *clientConfig, ref, status, note string) error {
	client, err := cfg.queryClient()
	if err != nil {
		return err
	}
	cfg.resolve()
	res, err := client.AnnotateDimm(cmd.Context(), connect.NewRequest(&pb.AnnotateRequest{Ref: ref, Status: status, Note: note, Actor: cfg.actor}))
	if err != nil {
		return rpcErr(err)
	}
	if cfg.json {
		return printJSON(cmd.OutOrStdout(), res.Msg)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n", when(res.Msg.Event.Ts), describe(res.Msg.Event))
	return nil
}

func showDimm(cmd *cobra.Command, cfg *clientConfig, ref string) error {
	client, err := cfg.queryClient()
	if err != nil {
		return err
	}
	res, err := client.GetDimm(cmd.Context(), connect.NewRequest(&pb.GetDimmRequest{Ref: ref}))
	if err != nil {
		return rpcErr(err)
	}
	if cfg.json {
		return printJSON(cmd.OutOrStdout(), res.Msg)
	}
	w := cmd.OutOrStdout()
	m := res.Msg
	ident := []string{m.Part, "serial " + m.Serial, "status " + m.Status}
	if len(m.Places) > 0 {
		d := m.Places[0].Dimm
		var spec []string
		for _, s := range []string{d.Manufacturer, memSize(d.SizeBytes), d.Type} {
			if s != "" && s != "-" {
				spec = append(spec, s)
			}
		}
		if d.SpeedMts > 0 {
			spec = append(spec, fmt.Sprintf("%d MT/s", d.SpeedMts))
		}
		if len(spec) > 0 {
			ident = append(ident, strings.Join(spec, " "))
		}
	}
	fmt.Fprintln(w, strings.Join(ident, "  "))
	if len(m.Places) > 0 {
		fmt.Fprintln(w)
		tw := tab(w)
		fmt.Fprintln(tw, "HOST\tSLOT\tCE\tUE\tFROM\tUNTIL")
		for _, p := range m.Places {
			until := "now"
			if p.GoneAt != nil {
				until = when(p.GoneAt)
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\n", p.Hostname, orDash(p.Dimm.Slot), p.Dimm.Ce, p.Dimm.Ue, when(p.FirstSeen), until)
		}
		tw.Flush()
	}
	if len(m.Events) > 0 {
		fmt.Fprintln(w)
		n := min(len(m.Events), 10)
		for _, e := range m.Events[:n] {
			fmt.Fprintf(w, "%s  %s\n", when(e.Ts), describe(e))
		}
	}
	return nil
}
