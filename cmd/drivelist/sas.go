package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/scottlaird/drivelist/collect"
	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
)

func newSASCmd(cfg *clientConfig) *cobra.Command {
	var errorsOnly bool
	var fixture, host, since string
	cmd := &cobra.Command{
		Use:   "sas [HOST | errors]",
		Short: "Show a host's SAS topology: HBAs, expanders, phys, link rates and error counters",
		Long: `sas prints every HBA and expander with its phys: which port each phy is
in, its negotiated link rate, what is on the other end (an expander, or
a drive with its bay), and the four SAS error counters the kernel keeps
per phy, cumulative since boot: invalid dwords, running disparity
errors, loss of dword sync, and phy reset problems. A wide port shows
as several phys sharing a port.

  drivelist sas             this host, straight from sysfs; no server or privileges needed
  drivelist sas HOST        HOST as the server last saw it, with the drive serial behind each phy
  drivelist sas errors      every phy in the fleet whose counters grew (--since 7d, --host H)`,
		Args: usageArgs(0, 1, "drivelist sas [HOST | errors] [--errors] [--fixture DIR]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && args[0] == "errors" {
				return sasErrors(cmd, cfg, host, since)
			}
			if len(args) == 1 {
				return sasFromServer(cmd, cfg, args[0], errorsOnly)
			}
			read := collectSAS
			if fixture != "" {
				read = collect.Fixture(fixture).SAS
			}
			topo, err := read()
			if err != nil {
				return err
			}
			if cfg.json {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(topo)
			}
			return printSAS(cmd.OutOrStdout(), topo, errorsOnly)
		},
	}
	cmd.Flags().BoolVar(&errorsOnly, "errors", false, "only phys with a nonzero error counter")
	cmd.Flags().StringVar(&fixture, "fixture", "", "read a captured tree (from drivelist capture) instead of this host")
	cmd.Flags().StringVar(&host, "host", "", "with errors: only this host")
	cmd.Flags().StringVar(&since, "since", "168h", "with errors: how far back, as a duration")
	return cmd
}

// sasFromServer prints a host's topology as the server holds it, rebuilt
// into the same shape the local walk produces so both print alike.
func sasFromServer(cmd *cobra.Command, cfg *clientConfig, host string, errorsOnly bool) error {
	client, err := cfg.queryClient()
	if err != nil {
		return err
	}
	res, err := client.GetSAS(cmd.Context(), connect.NewRequest(&pb.GetSASRequest{Host: host}))
	if err != nil {
		return rpcErr(err)
	}
	if cfg.json {
		return printJSON(cmd.OutOrStdout(), res.Msg)
	}
	topo := &collect.SASTopology{}
	nodeByAddr := map[string]*collect.SASNode{}
	nameOf := map[string]string{}
	for _, n := range res.Msg.Nodes {
		if n.GoneAt != nil {
			continue
		}
		node := &collect.SASNode{Kind: n.Node.Kind, Name: n.Node.Name, Address: n.Node.Address, Vendor: n.Node.Vendor, Product: n.Node.Product, Revision: n.Node.Revision, Upstream: n.Node.UpstreamPort}
		topo.Nodes = append(topo.Nodes, node)
		nodeByAddr[n.Node.Address] = node
		nameOf[n.Node.Address] = n.Node.Name
	}
	for _, n := range topo.Nodes {
		for _, m := range res.Msg.Nodes {
			if m.Node.Address == n.Address {
				n.Parent = nameOf[m.Node.ParentAddress]
			}
		}
	}
	for _, s := range res.Msg.Phys {
		if s.GoneAt != nil {
			continue
		}
		p, node := s.Phy, nodeByAddr[s.Phy.OwnerAddress]
		if node == nil {
			continue
		}
		node.Phys = append(node.Phys, &collect.SASPhy{Name: p.Name, ID: int(p.PhyId), Port: p.Port, Rate: p.Rate, Enabled: p.Enabled,
			InvalidDword: p.InvalidDword, DisparityErr: p.DisparityError, LossDwordSync: p.LossDwordSync, ResetProblem: p.PhyResetProblem})
		if p.Port == "" {
			continue
		}
		port := node.Port(p.Port)
		if port == nil {
			port = &collect.SASPort{Name: p.Port, Attached: p.Attached, NumPhys: int(p.PortWidth)}
			node.Ports = append(node.Ports, port)
			if p.AttachedKind == "drive" || p.AttachedKind == "device" {
				node.Devices = append(node.Devices, &collect.SASEndDevice{Name: p.Attached, Address: p.AttachedAddress, Port: p.Port, Bay: p.Bay, DevName: p.DevName, Protocols: s.Serial})
			}
		}
		port.Phys = append(port.Phys, p.Name)
	}
	return printSAS(cmd.OutOrStdout(), topo, errorsOnly)
}

func sasErrors(cmd *cobra.Command, cfg *clientConfig, host, since string) error {
	d, err := time.ParseDuration(since)
	if err != nil {
		return fmt.Errorf("--since: %w", err)
	}
	client, err := cfg.queryClient()
	if err != nil {
		return err
	}
	res, err := client.ListSASErrors(cmd.Context(), connect.NewRequest(&pb.ListSASErrorsRequest{Host: host, Since: timestamppb.New(time.Now().Add(-d))}))
	if err != nil {
		return rpcErr(err)
	}
	if cfg.json {
		return printJSON(cmd.OutOrStdout(), res.Msg)
	}
	if len(res.Msg.Rows) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "no SAS error counter growth in the last %s\n", since)
		return nil
	}
	tw := tab(cmd.OutOrStdout())
	fmt.Fprintln(tw, "HOST\tNODE\tPHY\tPORT\tATTACHED\tINVALID\tDISPARITY\tDWSYNC\tRESET\tREPORTS\tLAST")
	for _, r := range res.Msg.Rows {
		attached := r.Attached
		switch {
		case r.DevName != "":
			attached = r.DevName + " " + r.Serial
			if r.Bay != "" {
				attached += " bay " + r.Bay
			}
		case r.AttachedKind == "upstream":
			attached = "upstream"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\n", r.Hostname, orDash(r.OwnerName), r.PhyId, orDash(r.Port), orDash(attached),
			r.InvalidDword, r.DisparityError, r.LossDwordSync, r.PhyResetProblem, r.Samples, when(r.LastAt))
	}
	return tw.Flush()
}

// printSAS prints one block per node: a header naming it and where it
// hangs, then a row per phy.
func printSAS(w io.Writer, topo *collect.SASTopology, errorsOnly bool) error {
	if len(topo.Nodes) == 0 {
		fmt.Fprintln(w, "no SAS hosts")
		return nil
	}
	drives := map[string]string{} // node -> count summary
	for _, n := range topo.Nodes {
		disks := 0
		for _, d := range n.Devices {
			if d.DevName != "" {
				disks++
			}
		}
		drives[n.Name] = fmt.Sprintf("%d drives", disks)
	}
	first := true
	for _, n := range topo.Nodes {
		rows := sasRows(topo, n, errorsOnly)
		if errorsOnly && len(rows) == 0 {
			continue
		}
		if !first {
			fmt.Fprintln(w)
		}
		first = false
		where := ""
		if n.Parent != "" {
			where = fmt.Sprintf("  via %s %s", n.Parent, n.Upstream)
		}
		fmt.Fprintf(w, "%s  %s  %s  %d phys, %s%s\n", n.Name, strings.TrimSpace(n.Vendor+" "+n.Product+" "+n.Revision), orDash(n.Address), len(n.Phys), drives[n.Name], where)
		tw := tab(w)
		fmt.Fprintln(tw, "PHY\tPORT\tRATE\tATTACHED\tINVALID\tDISPARITY\tDWSYNC\tRESET")
		for _, r := range rows {
			fmt.Fprintln(tw, r)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return nil
}

func sasRows(topo *collect.SASTopology, n *collect.SASNode, errorsOnly bool) []string {
	var rows []string
	for _, p := range n.Phys {
		if errorsOnly && p.Errors() == 0 {
			continue
		}
		rate := "-"
		if p.Up() {
			rate = p.Rate
		} else if p.Rate != "" && p.Rate != "Unknown" {
			rate = strings.ToLower(p.Rate)
		}
		rows = append(rows, fmt.Sprintf("%d\t%s\t%s\t%s\t%d\t%d\t%d\t%d", p.ID, orDash(p.Port), rate, sasAttached(topo, n, p), p.InvalidDword, p.DisparityErr, p.LossDwordSync, p.ResetProblem))
	}
	return rows
}

// sasAttached says what is on the far end of a phy: the expander or the
// drive its port leads to, or "upstream" for an expander phy that has a
// link but no port of its own, which is how the link back towards the
// HBA looks from the expander's side.
func sasAttached(topo *collect.SASTopology, n *collect.SASNode, p *collect.SASPhy) string {
	if p.Port == "" {
		if p.Up() && n.Kind == "expander" {
			return "upstream"
		}
		return "-"
	}
	port := n.Port(p.Port)
	if port == nil {
		return "-"
	}
	if strings.HasPrefix(port.Attached, "expander-") {
		s := port.Attached
		if e := topo.Node(port.Attached); e != nil && e.Product != "" {
			s += " (" + strings.TrimSpace(e.Vendor+" "+e.Product) + ")"
		}
		if len(port.Phys) > 1 {
			s += fmt.Sprintf("  ×%d", len(port.Phys))
		}
		return s
	}
	for _, d := range n.Devices {
		if d.Name != port.Attached {
			continue
		}
		parts := []string{}
		if d.DevName != "" {
			parts = append(parts, d.DevName)
		}
		if d.Bay != "" {
			parts = append(parts, "bay "+d.Bay)
		}
		if d.DevName == "" {
			parts = append(parts, "no disk")
		} else if d.Protocols != "" {
			parts = append(parts, d.Protocols) // the protocol locally, the serial from the server
		}
		return strings.Join(parts, "  ")
	}
	return port.Attached
}
