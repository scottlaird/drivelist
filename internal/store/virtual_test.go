package store

import (
	"errors"
	"strings"
	"testing"
)

// TestVirtualMediaIgnored: a report from an older agent that lists a BMC's
// virtual disks gets no drive records for them, so the shared fake serial
// never makes one "drive" appear on every host.
func TestVirtualMediaIgnored(t *testing.T) {
	h := newHarness(t)
	virtual := func(name, model string) ReportDevice {
		return ReportDevice{DevName: name, Identity: DriveIdentity{Vendor: "AMI", Model: model, Serial: "AAAABBBBCCCC3"}, Bus: "usb"}
	}
	h.report(hostA, devX, virtual("sda", "Virtual_HDisk0"), virtual("sdb", "Virtual_HDisk1"))
	h.report(hostB, virtual("sda", "Virtual_HDisk0"))
	if n := h.count(`SELECT COUNT(*) FROM drive WHERE serial = 'AAAABBBBCCCC3'`); n != 0 {
		t.Errorf("virtual media drive records = %d, want 0", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM drive`); n != 1 {
		t.Errorf("drives = %d, want only the real one", n)
	}
}

// TestResolveSharedSerial: two drives that share a serial number can be
// named by MODEL/SERIAL, and the ambiguity error offers exactly those.
func TestResolveSharedSerial(t *testing.T) {
	h := newHarness(t)
	a := dev("sdc", "0000000001", "", "expander-4:0", "3")
	a.Identity.Model = "CheapSSD-A"
	b := dev("sdd", "0000000001", "", "expander-4:0", "4")
	b.Identity.Model = "CheapSSD-B"
	h.report(hostA, a, b)
	_, err := h.s.ResolveDrive(h.ctx, "0000000001")
	var amb *AmbiguousError
	if !errors.As(err, &amb) || len(amb.Candidates) != 2 || amb.Candidates[0] != "CheapSSD-A/0000000001" {
		t.Fatalf("shared serial = %v", err)
	}
	id, err := h.s.ResolveDrive(h.ctx, "CheapSSD-B/0000000001")
	if err != nil {
		t.Fatal(err)
	}
	ds, _ := h.s.ListDrives(h.ctx, DriveFilter{})
	for _, d := range ds {
		if d.ID == id && d.Model != "CheapSSD-B" {
			t.Errorf("MODEL/SERIAL resolved to %s", d.Model)
		}
	}
	if ev, err := h.s.Annotate(h.ctx, "CheapSSD-A/0000000001", StatusRetired, "", "tester"); err != nil || !strings.Contains(ev.Detail, `"status":"retired"`) {
		t.Errorf("mark by MODEL/SERIAL = %+v, %v", ev, err)
	}
}
