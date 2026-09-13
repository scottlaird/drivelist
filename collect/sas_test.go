package collect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestSASTopology(t *testing.T) {
	topo, err := Fixture("testdata/synthetic").SAS()
	if err != nil {
		t.Fatal(err)
	}
	if len(topo.Nodes) != 2 || topo.Nodes[0].Name != "host4" || topo.Nodes[1].Name != "expander-4:0" {
		t.Fatalf("nodes = %+v", topo.Nodes)
	}
	hba, exp := topo.Nodes[0], topo.Nodes[1]
	if hba.Kind != "hba" || hba.Address != "0x500605b00a1b2c00" || hba.Product != "SAS9300-8i" || hba.Revision != "16.00.12.00" || hba.Vendor != "mpt3sas" {
		t.Errorf("hba = %+v", hba)
	}
	if len(hba.Phys) != 8 || len(hba.Ports) != 1 {
		t.Fatalf("hba phys %d ports %d", len(hba.Phys), len(hba.Ports))
	}
	// The wide port: four phys at 12G to the expander; phy 3 has errors.
	port := hba.Ports[0]
	if diff := cmp.Diff(&SASPort{Name: "port-4:0", Phys: []string{"phy-4:0", "phy-4:1", "phy-4:2", "phy-4:3"}, NumPhys: 4, Attached: "expander-4:0"}, port); diff != "" {
		t.Errorf("hba port (-want +got):\n%s", diff)
	}
	for _, p := range hba.Phys[:4] {
		if p.Port != "port-4:0" || p.Rate != "12.0 Gbit" || p.Gbit() != 12 || !p.Up() {
			t.Errorf("phy %s = %+v", p.Name, p)
		}
	}
	if p := hba.Phys[3]; p.InvalidDword != 12 || p.DisparityErr != 3 || p.Errors() != 15 {
		t.Errorf("phy-4:3 counters = %+v", p)
	}
	if p := hba.Phys[5]; p.Port != "" || p.Up() || p.Rate != "Unknown" {
		t.Errorf("unattached phy = %+v", p)
	}

	if exp.Kind != "expander" || exp.Parent != "host4" || exp.Upstream != "port-4:0" || exp.Address != "0x500605b00a1b2c3d" || exp.Vendor != "LSI" || exp.Product != "SAS2X36" || exp.Revision != "0e0b" {
		t.Errorf("expander = %+v", exp)
	}
	if len(exp.Phys) != 12 || len(exp.Ports) != 3 || len(exp.Devices) != 3 {
		t.Fatalf("expander phys %d ports %d devices %d", len(exp.Phys), len(exp.Ports), len(exp.Devices))
	}
	// Phys 0-3 carry the upstream link and belong to no port here.
	if p := exp.Phys[0]; p.Port != "" || !p.Up() {
		t.Errorf("upstream phy = %+v", p)
	}
	byDev := topo.DevicePhys()
	if phys := byDev["sda"]; len(phys) != 1 || phys[0].Name != "phy-4:0:4" || phys[0].Rate != "12.0 Gbit" {
		t.Errorf("sda phys = %+v", phys)
	}
	if phys := byDev["sdb"]; len(phys) != 1 || phys[0].Name != "phy-4:0:5" || phys[0].Rate != "6.0 Gbit" || phys[0].LossDwordSync != 7 {
		t.Errorf("sdb phys = %+v", phys)
	}
	want := []*SASEndDevice{
		{Name: "end_device-4:0:0", Address: "0x5000cca25206c809", Port: "port-4:0:0", Bay: "0", Enclosure: "0x500605b00a1b2c3e", Protocols: "ssp", DevName: "sda"},
		{Name: "end_device-4:0:1", Address: "0x5000cca260c165e3", Port: "port-4:0:1", Bay: "1", Enclosure: "0x500605b00a1b2c3e", Protocols: "stp", DevName: "sdb"},
		{Name: "end_device-4:0:2", Address: "0x500605b00a1b2c3f", Port: "port-4:0:2", Bay: "2", Enclosure: "0x500605b00a1b2c3e", Protocols: "none"},
	}
	if diff := cmp.Diff(want, exp.Devices); diff != "" {
		t.Errorf("end devices (-want +got):\n%s", diff)
	}
}

// TestSASWithoutTransport: an NVMe-only host has no sas_host class.
func TestSASWithoutTransport(t *testing.T) {
	topo, err := Fixture("testdata/mgmt1").SAS()
	if err != nil || len(topo.Nodes) != 0 {
		t.Errorf("SAS on mgmt1 = %+v, %v", topo, err)
	}
}

// TestCaptureSASRoundTrip: capturing the synthetic fixture reproduces the
// same topology, symlinks included.
func TestCaptureSASRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := Fixture("testdata/synthetic")
	if err := src.captureSAS(dir); err != nil {
		t.Fatal(err)
	}
	// The block device directories come from the rest of Capture; put
	// them where the topology expects them.
	for _, rel := range []string{
		"devices/pci0000:00/0000:00:01.0/0000:01:00.0/host4/port-4:0/expander-4:0/port-4:0:0/end_device-4:0:0/target4:0:0/4:0:0:0/block/sda",
		"devices/pci0000:00/0000:00:01.0/0000:01:00.0/host4/port-4:0/expander-4:0/port-4:0:1/end_device-4:0:1/target4:0:1/4:0:1:0/block/sdb",
	} {
		if err := os.MkdirAll(filepath.Join(dir, "sys", rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	want, _ := src.SAS()
	got, err := Fixture(dir).SAS()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got, cmp.FilterPath(func(p cmp.Path) bool { return p.Last().String() == ".Path" }, cmp.Ignore())); diff != "" {
		t.Errorf("round trip (-want +got):\n%s", diff)
	}
}
