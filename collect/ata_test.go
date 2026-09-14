package collect

import (
	"path/filepath"
	"testing"

	"github.com/scottlaird/drivelist"
)

func TestATAPort(t *testing.T) {
	cases := map[string]*drivelist.Device{
		"pci-0000:00:1f.2-ata-5": {Attribs: map[string]string{"ID_PATH": "pci-0000:00:1f.2-ata-5.0"}},
		"pci-0000:00:17.0-ata-1": {Attribs: map[string]string{"ID_PATH": "pci-0000:00:17.0-ata-1"}},
		"ata3":                   {SysPath: "x/0000:00:17.0/ata3/host2/target2:0:0/2:0:0:0/block/sdc"},
		"":                       {Attribs: map[string]string{"ID_PATH": "pci-0000:00:14.0-usb-0:3:1.0-scsi-0:0:0:0"}, SysPath: "x/usb1/1-3/block/sdd"},
	}
	for want, d := range cases {
		if got := ataPort(d); got != want {
			t.Errorf("ataPort(%+v) = %q, want %q", d, got, want)
		}
	}
}

// TestATAPorts: the synthetic host's SATA drive on the onboard controller
// gets the chassis as its enclosure and its port as the bay; the fixtures
// captured before DMI was recorded have no chassis key and place nothing
// on ports, and drives in SES bays are never touched.
func TestATAPorts(t *testing.T) {
	inv, err := Fixture(filepath.Join("testdata", "synthetic")).Collect()
	if err != nil {
		t.Fatal(err)
	}
	var sdc *drivelist.Device
	for _, d := range inv.Devices {
		if d.DeviceName == "sdc" {
			sdc = d
		}
		if d.DeviceName == "sda" && d.EnclosureVia != "expander-4:0" {
			t.Errorf("sda lost its SES place: %+v", d)
		}
	}
	if sdc == nil || sdc.EnclosureVia != "ata" || sdc.EnclosureBay != "ata3" || sdc.EnclosureID != "dmi:SYN-0001" || sdc.EnclosureModel != "Synthetic Systems Testbox" {
		t.Errorf("sdc = %+v", sdc)
	}
	inv, err = Fixture(filepath.Join("testdata", "fs2")).Collect()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range inv.Devices {
		if d.EnclosureVia == "ata" {
			t.Errorf("fs2 has no DMI capture but %s got an ATA place", d.DeviceName)
		}
	}
}
