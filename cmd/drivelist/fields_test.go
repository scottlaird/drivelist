package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/scottlaird/drivelist"
)

func TestFieldListSet(t *testing.T) {
	var f fieldList
	if err := f.Set("serial,bay"); err != nil {
		t.Fatalf("Set(serial,bay): %v", err)
	}
	if got, want := f.String(), "serial,bay"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if err := f.Set("serial,nosuchfield"); err == nil {
		t.Error("Set(serial,nosuchfield): got nil error, want error")
	}
}

func TestTable(t *testing.T) {
	devices := []*drivelist.Device{
		{DeviceName: "sda", Serial: "S1", EnclosureBay: "0", Size: 8_000_000_000_000},
		{DeviceName: "sdb", Serial: "S2", EnclosureBay: "17"},
	}
	// Written straight to a buffer, without the tabwriter, so the cells
	// are checked rather than the alignment padding.
	var buf bytes.Buffer
	if err := table(&buf, []string{"devicename", "serial", "bay", "size"}, devices); err != nil {
		t.Fatalf("table: %v", err)
	}
	want := strings.Join([]string{
		"Device Name\tSerial\tBay\tSize\t",
		"===========\t======\t===\t====\t",
		"sda\tS1\t0\t8 TB\t",
		"sdb\tS2\t17\t\t",
		"",
	}, "\n")
	if got := buf.String(); got != want {
		t.Errorf("table output mismatch\n got: %q\nwant: %q", got, want)
	}
}
