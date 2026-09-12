package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/scottlaird/drivelist"
)

// column is one printable attribute of a device.
type column struct {
	name   string // as given to --fields
	header string
	value  func(*drivelist.Device) string
}

// columns lists every column in the order --allfields prints them.
var columns = []column{
	{"devicename", "Device Name", func(d *drivelist.Device) string { return d.DeviceName }},
	{"devices", "Devices", func(d *drivelist.Device) string { return strings.Join(d.Devices, ",") }},
	{"wwn", "WWN", func(d *drivelist.Device) string { return d.WWN }},
	{"syspath", "Sys Path", func(d *drivelist.Device) string { return d.SysPath }},
	{"model", "Model", func(d *drivelist.Device) string { return d.Model }},
	{"serial", "Serial", func(d *drivelist.Device) string { return d.Serial }},
	{"uses", "Uses", func(d *drivelist.Device) string { return strings.Join(d.Uses, ",") }},
	{"genericdevice", "Generic Device", func(d *drivelist.Device) string { return d.GenericDevice }},
	{"expander", "Expander", func(d *drivelist.Device) string { return d.Expander }},
	{"expanderpath", "Expander Path", func(d *drivelist.Device) string { return d.ExpanderPath }},
	{"bay", "Bay", func(d *drivelist.Device) string { return d.EnclosureBay }},
	{"size", "Size", func(d *drivelist.Device) string { return drivelist.FormatDiskSize(d.Size) }},
	{"error", "Error", func(d *drivelist.Device) string { return d.Error }},
}

var defaultFields = []string{"devicename", "model", "wwn", "serial", "expander", "bay", "size"}

func allFieldNames() []string {
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = c.name
	}
	return names
}

// fieldList is the flag.Value behind --fields: a comma-separated list of
// column names, validated on Set.
type fieldList []string

func (f *fieldList) String() string { return strings.Join(*f, ",") }

// Type names the flag value in help output, as pflag.Value requires.
func (f *fieldList) Type() string { return "fields" }

func (f *fieldList) Set(value string) error {
	var fields []string
	for _, name := range strings.Split(value, ",") {
		if _, err := columnByName(name); err != nil {
			return err
		}
		fields = append(fields, name)
	}
	*f = fields
	return nil
}

func columnByName(name string) (column, error) {
	for _, c := range columns {
		if c.name == name {
			return c, nil
		}
	}
	return column{}, fmt.Errorf("unknown field %q (valid: %s)", name, strings.Join(allFieldNames(), ","))
}

// table writes a header, an underline, and one tab-separated row per device
// for the named fields. The writer is expected to be a tabwriter.
func table(w io.Writer, fields []string, devices []*drivelist.Device) error {
	cols := make([]column, len(fields))
	for i, name := range fields {
		c, err := columnByName(name)
		if err != nil {
			return err
		}
		cols[i] = c
	}
	var header, underline strings.Builder
	for _, c := range cols {
		header.WriteString(c.header + "\t")
		underline.WriteString(strings.Repeat("=", len(c.header)) + "\t")
	}
	fmt.Fprintln(w, header.String())
	fmt.Fprintln(w, underline.String())
	for _, d := range devices {
		for _, c := range cols {
			fmt.Fprint(w, c.value(d), "\t")
		}
		fmt.Fprintln(w)
	}
	return nil
}
