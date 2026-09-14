package collect

import (
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/scottlaird/drivelist"
)

// TestParseSMBIOSSlot decodes the four type 9 records of a Minisforum
// MS-A2 as the kernel exposes them: one M.2 connector named by its
// reference designator, two x4 slots, and a PCIE1 whose bridge address
// names nothing that exists.
func TestParseSMBIOSSlot(t *testing.T) {
	records := map[string]smbiosSlot{
		"0911220001a90b030301000401000000ff50434945310000": {Designation: "PCIE1", Address: "0000:00:1f.7", InUse: false},
		"0911230001a80a0403000004010000000a4a333530320000": {Designation: "J3502", Address: "0000:00:01.2", InUse: true},
		"0911240001a80a0403060004010000001150434945340000": {Designation: "PCIE4", Address: "0000:00:02.1", InUse: true},
		"0911250001a80a0403360004010000001250434945330000": {Designation: "PCIE3", Address: "0000:00:02.2", InUse: true},
		"09110100010603030300000000000000004d2e325f310000": {Designation: "M.2_1", Address: "0000:00:00.0", InUse: false},
		"090c010001060303030000004d2e325f320000":           {Designation: "M.2_2", InUse: false}, // SMBIOS 2.5: no address
		"0a11010001a80a0403000004010000000a4a333530320000": {},                                   // not type 9
	}
	for raw, want := range records {
		b, err := hex.DecodeString(raw)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := parseSMBIOSSlot(b)
		if ok != (want.Designation != "") || got != want {
			t.Errorf("%s: got %+v ok=%v, want %+v", raw[:8], got, ok, want)
		}
	}
}

// TestSMBIOSSlotFallback: with no hotplug slot table, the synthetic host's
// NVMe drive is placed by the SMBIOS record naming the root port above
// its controller.
func TestSMBIOSSlotFallback(t *testing.T) {
	c := Fixture(filepath.Join("testdata", "synthetic"))
	if got := readSMBIOSSlots(c.sys()); len(got) != 2 || got["0000:00:1b.0"].Designation != "M.2_1" {
		t.Fatalf("readSMBIOSSlots = %+v", got)
	}
	inv, err := c.Collect()
	if err != nil {
		t.Fatal(err)
	}
	var nvme *drivelist.Device
	for _, d := range inv.Devices {
		if d.DeviceName == "nvme0n1" {
			nvme = d
		}
		if d.IsEmptyBay() && d.EnclosureVia == "pci" {
			t.Errorf("SMBIOS produced an empty bay: %+v", d)
		}
	}
	want := &drivelist.Device{EnclosureBay: "M.2_1", EnclosureVia: "pci", EnclosureID: "dmi:SYN-0001", EnclosureViaID: "dmi:SYN-0001", EnclosureModel: "Synthetic Systems Testbox"}
	got := &drivelist.Device{EnclosureBay: nvme.EnclosureBay, EnclosureVia: nvme.EnclosureVia, EnclosureID: nvme.EnclosureID, EnclosureViaID: nvme.EnclosureViaID, EnclosureModel: nvme.EnclosureModel}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("nvme0n1 location (-want +got):\n%s", diff)
	}
	if anc := c.pciAncestors(nvme.SysPath, "0000:02:00.0"); len(anc) != 1 || anc[0] != "0000:00:1b.0" {
		t.Errorf("pciAncestors = %v", anc)
	}
}

// TestBridgeFallback: a drive in no hotplug slot whose bridge SMBIOS does
// not describe is placed by the bridge's address, so a profile can still
// name the slot. mgmt1's onboard M.2 is the case: no slot, no record.
func TestBridgeFallback(t *testing.T) {
	inv, err := Fixture(filepath.Join("testdata", "mgmt1")).Collect()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range inv.Devices {
		if d.DeviceName != "nvme2n1" {
			continue
		}
		if d.EnclosureVia != "pci" || d.EnclosureBay == "" || d.EnclosureBay == "9-1" {
			t.Errorf("nvme2n1 = %+v", d)
		}
		t.Logf("nvme2n1 placed by bridge %s", d.EnclosureBay)
		return
	}
	t.Error("nvme2n1 not in inventory")
}
