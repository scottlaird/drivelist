package main

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/scottlaird/drivelist/internal/report"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "drivelist %s (%s, %s/%s)\n", report.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		},
	}
}
