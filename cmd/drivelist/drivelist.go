// Command drivelist lists the drives in this system, where they are, and
// what they are used for.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/scottlaird/drivelist"
	"github.com/scottlaird/drivelist/collect"
)

var (
	unusedFlag    = flag.Bool("unused", false, "Only show unused devices")
	allFieldsFlag = flag.Bool("allfields", false, "Show all fields (will be wide)")
	ledctl        = flag.String("ledctl", "", "Call ledctl instead of listing drives.  Use --unused --ledctl=locate for ledctl --locate=<unused drives>")
	fields        = fieldList(defaultFields)
)

func main() {
	flag.Var(&fields, "fields", "Comma-separated list of fields to show")
	flag.Parse()

	if *allFieldsFlag {
		fields = allFieldNames()
	}

	inv, err := collect.All()
	if err != nil {
		fmt.Fprintf(os.Stderr, "drivelist: %v\n", err)
		os.Exit(1)
	}

	var unused []*drivelist.Device
	for _, d := range inv.Devices {
		if d.Unused() {
			unused = append(unused, d)
		}
	}

	if *ledctl != "" {
		var names []string
		for _, d := range unused {
			names = append(names, d.DeviceName)
		}
		fmt.Printf("ledctl %s=%s\n", *ledctl, strings.Join(names, ","))
		return
	}

	devices := inv.Devices
	if *unusedFlag {
		devices = unused
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 8, 1, '\t', 0)
	if err := table(w, fields, devices); err != nil {
		fmt.Fprintf(os.Stderr, "drivelist: %v\n", err)
		os.Exit(1)
	}
	w.Flush()
}
