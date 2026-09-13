package main

import (
	"strings"
	"testing"

	"github.com/scottlaird/drivelist/internal/report"
)

func TestSASFleet(t *testing.T) {
	fleetEnv(t)
	mustRun(t, "report")
	host := report.Host().Hostname
	out := mustRun(t, "sas", host)
	for _, want := range []string{"host4  mpt3sas SAS9300-8i 16.00.12.00", "expander-4:0 (LSI SAS2X36)  ×4", "sdb  bay 1  VLG32AEY", "upstream", "6.0 Gbit"} {
		if !strings.Contains(out, want) {
			t.Errorf("sas %s lacks %q:\n%s", host, want, out)
		}
	}
	if out := mustRun(t, "sas", "errors"); !strings.Contains(out, "no SAS error counter growth") {
		t.Errorf("sas errors: %q", out)
	}
	if _, _, err := run(t, "sas", "nosuchhost"); err == nil {
		t.Error("sas for an unknown host succeeded")
	}
	if out := mustRun(t, "events"); !strings.Contains(out, "sas node      ") || !strings.Contains(out, "expander-4:0 (LSI SAS2X36) appeared") {
		t.Errorf("events lack the node appearances:\n%s", out)
	}
}
