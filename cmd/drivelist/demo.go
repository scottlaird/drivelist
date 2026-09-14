package main

import (
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/scottlaird/drivelist/internal/demo"
)

func newDemoCmd(cfg *clientConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "demo",
		Short: "Build a static, sanitized copy of the web interface",
	}
	var namesFile string
	export := &cobra.Command{
		Use:   "export DIR",
		Short: "Write the web interface and the fleet's data as static files into DIR",
		Long: `export asks the server everything the web interface can ask and writes
each answer as a file under DIR/data, with the page itself marked as a
demo so it reads those files instead of a server, its clock stopped at
the export. Names are changed as the --names file says: hosts it lists
by name, every other host as server1, server2, ... (kept stable across
exports through DIR/mapping.json), literal replacements applied to
every string, and fields it names emptied. Publish DIR with
demo/publish.sh, or serve it from anywhere that serves files.`,
		Args: usageArgs(1, 1, "drivelist demo export DIR [--names FILE]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var names demo.Names
			if namesFile != "" {
				var err error
				if names, err = demo.ReadNames(namesFile); err != nil {
					return err
				}
			}
			client, err := cfg.queryClient()
			if err != nil {
				return err
			}
			log := func(msg string, kv ...any) { slog.Info(msg, kv...) }
			man, err := demo.Export(cmd.Context(), client, args[0], names, log)
			if err != nil {
				return rpcErr(err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "drivelist: wrote %d files for %d hosts and %d drives to %s\n", man.Files, man.Hosts, man.Drives, args[0])
			return nil
		},
	}
	export.Flags().StringVar(&namesFile, "names", "", "JSON file saying how to rename hosts and rewrite strings (see demo/names.json)")
	cmd.AddCommand(export)
	return cmd
}
