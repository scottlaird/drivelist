package main

import (
	"strings"
	"testing"
)

func TestSASCommand(t *testing.T) {
	useFixture(t)
	out, _, err := run(t, "sas")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"host4  mpt3sas SAS9300-8i 16.00.12.00  0x500605b00a1b2c00  8 phys, 0 drives",
		"expander-4:0 (LSI SAS2X36)  ×4",
		"expander-4:0  LSI SAS2X36 0e0b  0x500605b00a1b2c3d  12 phys, 2 drives  via host4 port-4:0",
		"upstream",
		"sda  bay 0  ssp",
		"sdb  bay 1  stp",
		"bay 2  no disk",
		"6.0 Gbit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("sas output lacks %q:\n%s", want, out)
		}
	}
	// --errors keeps only the two phys with counters: host phy 3 and
	// expander phy 5.
	out, _, err = run(t, "sas", "--errors")
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "3 ") || strings.HasPrefix(line, "5 ") {
			rows++
		}
	}
	if rows != 2 || strings.Contains(out, "upstream") || !strings.Contains(out, "12") || !strings.Contains(out, "7") {
		t.Errorf("sas --errors:\n%s", out)
	}
	if out, _, err := run(t, "--json", "sas"); err != nil || !strings.Contains(out, `"Name": "phy-4:0:5"`) {
		t.Errorf("sas --json: %v\n%s", err, out)
	}
}
