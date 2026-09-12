// Command drivelist lists the drives in this system, where they are, and
// what they are used for. With no subcommand it prints the listing; the
// subcommands run the fleet server, agent, and queries.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "drivelist: %v\n", err)
		os.Exit(1)
	}
}
