package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/scottlaird/drivelist"
	"github.com/scottlaird/drivelist/collect"
)

// collectAll is what the listing runs; tests point it at a fixture.
var collectAll = collect.All

// listOptions are the flags of the bare drivelist command.
type listOptions struct {
	unused    bool
	allFields bool
	fields    fieldList
	ledctl    string
}

func newRootCmd() *cobra.Command {
	opts := listOptions{fields: fieldList(defaultFields)}
	var captureDir string
	cfg := &clientConfig{}

	cmd := &cobra.Command{
		Use:   "drivelist",
		Short: "List the drives in this system and what they are used for",
		Long: `drivelist lists every drive in this system with its identity, its
physical location (SAS expander and bay) and what it is used for (ZFS
pool membership, mounts). With no subcommand it prints that listing.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if captureDir != "" {
				return runCapture(captureDir)
			}
			return runList(cmd.OutOrStdout(), cmd.ErrOrStderr(), opts)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.unused, "unused", false, "only show unused devices")
	f.BoolVar(&opts.allFields, "allfields", false, "show all fields (will be wide)")
	f.Var(&opts.fields, "fields", "comma-separated list of fields to show")
	f.StringVar(&opts.ledctl, "ledctl", "", "print a ledctl command for the unused devices instead of listing them, e.g. --unused --ledctl=locate")
	f.StringVar(&captureDir, "capture", "", "")
	_ = f.MarkDeprecated("capture", "use 'drivelist capture DIR'")

	pf := cmd.PersistentFlags()
	pf.StringVar(&cfg.server, "server", "", "fleet server, host:port or URL (also DRIVELIST_SERVER or the config file)")
	pf.BoolVar(&cfg.json, "json", false, "print the server's response as JSON")

	cmd.AddCommand(newCaptureCmd(), newServeCmd())
	cmd.AddCommand(fleetCommands(cfg)...)
	return cmd
}

// runList prints the listing for the flags in opts. Devices that could not
// be identified, and pool members with no device, go to errw first.
func runList(w, errw io.Writer, opts listOptions) error {
	fields := []string(opts.fields)
	if opts.allFields {
		fields = allFieldNames()
	}

	inv, err := collectAll()
	if err != nil {
		return err
	}
	for _, d := range inv.Degraded() {
		fmt.Fprintf(errw, "drivelist: %s could not be identified: %s\n", d.DeviceName, d.Error)
	}
	for _, m := range inv.Unmapped {
		fmt.Fprintf(errw, "drivelist: pool %s expects %s (%s) but no such device is present; perhaps a drive failed completely or was removed\n", m.Pool, m.Path, m.State)
	}

	var unused []*drivelist.Device
	for _, d := range inv.Devices {
		if d.Unused() {
			unused = append(unused, d)
		}
	}

	if opts.ledctl != "" {
		var names []string
		for _, d := range unused {
			names = append(names, d.DeviceName)
		}
		fmt.Fprintf(w, "ledctl %s=%s\n", opts.ledctl, strings.Join(names, ","))
		return nil
	}

	devices := inv.Devices
	if opts.unused {
		devices = unused
	}
	tw := tabwriter.NewWriter(w, 0, 8, 1, '\t', 0)
	if err := table(tw, fields, devices); err != nil {
		return err
	}
	return tw.Flush()
}

func newCaptureCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "capture DIR",
		Short: "Write this system's collector inputs to DIR as a test fixture",
		Long: `capture records everything the collector reads from this system into
DIR: the relevant parts of sysfs, the mount table, udevadm output for
every disk, and the output of several zpool commands. The tree can be
loaded with collect.Fixture and is what collect/testdata/ holds. It
includes the serial number and WWN of every drive.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runCapture(args[0])
		},
	}
}

func runCapture(dir string) error {
	if err := (&collect.Collector{}).Capture(dir); err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	return nil
}
