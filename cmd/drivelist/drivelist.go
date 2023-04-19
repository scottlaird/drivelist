package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"github.com/scottlaird/drivelist"

	//"sort"
	"strings"
	"text/tabwriter"
)

var (
	unusedFlag    = flag.Bool("unused", false, "Only show unused devices")
	allFieldsFlag = flag.Bool("allfields", false, "Show all fields (will be wide)")
	ledctl        = flag.String("ledctl", "", "Call ledctl instead of listing drives.  Use --unused --ledctl=locate for ledctl --locate=<unused drives>")
)

func main() {
	flag.Parse()

	if *allFieldsFlag {
		drivelist.FieldFlag = drivelist.Fields
	}

	disks, err := drivelist.GetAllDisks()
	if err != nil {
		panic(err)
	}

	var unused []*drivelist.DiskDevice
	for _, disk := range disks.Devices {
		if len(disk.Uses) == 0 {
			unused = append(unused, disk)
		}
	}

	if len(*ledctl) > 0 {
		devices := []string{}
		for _, d := range unused {
			devices = append(devices, d.DeviceName)
		}
		fmt.Printf("ledctl %s=%s\n", *ledctl, strings.Join(devices, ","))
		return
	}

	//for expanderName, _ := range bays {
	//fmt.Printf("Expander %q\n", expanderName)
	//		bayNames := []string{}
	//		for n, _ := range bays[expanderName] {
	//			bayNames = append(bayNames, n)
	//		}
	//		sort.Strings(bayNames)
	//		for _, n := range bayNames {
	//			fmt.Printf("  %s\n", n)
	//		}
	//	}

	writer := tabwriter.NewWriter(os.Stdout, 0, 8, 1, '\t', 0)

	buf := bytes.Buffer{} // Header

	for _, f := range drivelist.FieldFlag {
		switch f {
		case "devicename":
			buf.WriteString("Device Name\t")
		case "devices":
			buf.WriteString("Devices\t")
		case "wwn":
			buf.WriteString("WWN\t")
		case "syspath":
			buf.WriteString("Sys Path\t")
		case "model":
			buf.WriteString("Model\t")
		case "serial":
			buf.WriteString("Serial\t")
		case "uses":
			buf.WriteString("Uses\t")
		case "genericdevice":
			buf.WriteString("Generic Device\t")
		case "expander":
			buf.WriteString("Expander\t")
		case "expanderpath":
			buf.WriteString("Expander Path\t")
		case "bay":
			buf.WriteString("Bay\t")
		case "size":
			buf.WriteString("Size\t")
		default:
			panic(fmt.Sprintf("unknown field %q", f))
		}
	}

	buf.WriteString("\n")
	b2 := buf.String()
	buf.WriteTo(writer)

	div := strings.Map(func(r rune) rune {
		if r < ' ' {
			return r
		} else {
			return '='
		}
	}, b2)
	fmt.Fprintf(writer, div)

	displayDisks := disks.Devices
	if *unusedFlag {
		displayDisks = unused
	}

	for _, disk := range displayDisks {
		disk.WriteTabs(writer)
	}

	writer.Flush()

}

// TODO
//  - Include empty bays.
//    - Scan through <enclosure> directories looking for <bay_ident> subdirs, and gather all (enclosure,bay_ident) pairs.
//    - Create "fake" drives for unoccupied bays.
//    - Add --empty and --all flags; empty only shows empty bays, while --all skips all filters.
//  - Include size.
//    - Add flag for rounding
//  - Add sorting
//    - Use a flag like --fields, but for sorting.
//  - Add CSV output
