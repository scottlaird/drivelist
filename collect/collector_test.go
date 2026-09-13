package collect

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/scottlaird/drivelist"
)

const zfsUse = "zfs > space 5925914041408872576 > raidz2 1731578064309998343 > disk 1837064446111292648"

// fakeZFS stands in for libzfs: it marks the by-vdev name the pool would
// report, exercising the same ByName lookup the real annotator uses.
func fakeZFS(inv *drivelist.Inventory) error {
	d := inv.ByName("/dev/disk/by-vdev/Ab0")
	if d == nil {
		return errors.New("fakeZFS: /dev/disk/by-vdev/Ab0 not in inventory")
	}
	d.Uses = append(d.Uses, zfsUse)
	return nil
}

// wantSynthetic is what Collect should produce from testdata/synthetic when
// its sysfs root is sys: an NVMe boot drive mounted through its second
// partition, two SAS disks in bays 0 and 1 of one expander (one in a pool,
// one mounted through a partition), a direct-attached SATA SSD with no use,
// a disk whose udevadm output is missing, and the expander's empty bay 2.
func wantSynthetic(sys string) []*drivelist.Device {
	exp := sys + "/devices/pci0000:00/0000:00:01.0/0000:01:00.0/host4/port-4:0/expander-4:0"
	return []*drivelist.Device{
		{
			DeviceName: "nvme0n1",
			Devices:    []string{"/dev/nvme0n1", "/dev/disk/by-id/nvme-Samsung_SSD_980_PRO_1TB_S5GXNX0T123456B", "/dev/disk/by-id/nvme-eui.002538b311b12345"},
			WWN:        "eui.002538b311b12345",
			SysPath:    sys + "/devices/pci0000:00/0000:00:1b.0/0000:02:00.0/nvme/nvme0/nvme0n1",
			Model:      "Samsung_SSD_980_PRO_1TB",
			Serial:     "S5GXNX0T123456B",
			Uses:       []string{"mount > /"},
			Size:       1000204886016,

			EnclosureBay:   "M.2_1",
			EnclosureVia:   "pci",
			EnclosureID:    "dmi:SYN-0001",
			EnclosureViaID: "dmi:SYN-0001",
			EnclosureModel: "Synthetic Systems Testbox",
		},
		{
			DeviceName:     "sda",
			Devices:        []string{"/dev/sda", "/dev/disk/by-id/wwn-0x5000cca25206c808", "/dev/disk/by-id/scsi-35000cca25206c808", "/dev/disk/by-vdev/Ab0"},
			WWN:            "0x5000cca25206c808",
			SysPath:        exp + "/port-4:0:0/end_device-4:0:0/target4:0:0/4:0:0:0/block/sda",
			Model:          "HUH721008AL5204",
			Serial:         "7SG3RM2G",
			Uses:           []string{zfsUse},
			GenericDevice:  "/dev/bsg/end_device-4:0:0",
			Expander:       "expander-4:0",
			ExpanderID:     "0x500605b00a1b2c3d",
			ExpanderPath:   exp,
			EnclosureBay:   "0",
			EnclosureID:    "0x500605b00a1b2c3e",
			EnclosureVia:   "expander-4:0",
			EnclosureViaID: "0x500605b00a1b2c3d",
			Size:           8001563222016,
		},
		{
			DeviceName:     "sdb",
			Devices:        []string{"/dev/sdb", "/dev/disk/by-id/wwn-0x5000cca260c165e2", "/dev/disk/by-id/scsi-35000cca260c165e2"},
			WWN:            "0x5000cca260c165e2",
			SysPath:        exp + "/port-4:0:1/end_device-4:0:1/target4:0:1/4:0:1:0/block/sdb",
			Model:          "HUH728080ALE601",
			Serial:         "VLG32AEY",
			Uses:           []string{"mount > /backup"},
			GenericDevice:  "/dev/bsg/end_device-4:0:1",
			Expander:       "expander-4:0",
			ExpanderID:     "0x500605b00a1b2c3d",
			ExpanderPath:   exp,
			EnclosureBay:   "1",
			EnclosureID:    "0x500605b00a1b2c3e",
			EnclosureVia:   "expander-4:0",
			EnclosureViaID: "0x500605b00a1b2c3d",
			Size:           8001563222016,
		},
		{
			DeviceName: "sdc",
			Devices:    []string{"/dev/sdc", "/dev/disk/by-id/ata-Samsung_SSD_870_EVO_1TB_S5RRNF0R123456A", "/dev/disk/by-id/wwn-0x5002538f41234567"},
			WWN:        "0x5002538f41234567",
			SysPath:    sys + "/devices/pci0000:00/0000:00:17.0/ata3/host2/target2:0:0/2:0:0:0/block/sdc",
			Model:      "Samsung_SSD_870_EVO_1TB",
			Serial:     "S5RRNF0R123456A",
			Uses:       []string{},
			Size:       1000204886016,

			EnclosureBay:   "ata3",
			EnclosureVia:   "ata",
			EnclosureID:    "dmi:SYN-0001",
			EnclosureViaID: "dmi:SYN-0001",
			EnclosureModel: "Synthetic Systems Testbox",
		},
		{
			DeviceName: "sdd",
			Devices:    []string{"/dev/sdd"},
			Uses:       []string{},
			Error:      "udevadm: open testdata/synthetic/exec/udevadm info --query=all --name=_dev_sdd: no such file or directory",
		},
		{
			Expander:       "expander-4:0",
			ExpanderID:     "0x500605b00a1b2c3d",
			ExpanderPath:   exp,
			EnclosureBay:   "2",
			EnclosureID:    "0x500605b00a1b2c3e",
			EnclosureVia:   "expander-4:0",
			EnclosureViaID: "0x500605b00a1b2c3d",
			Uses:           []string{"empty"},
		},
	}
}

var ignoreAttribs = cmpopts.IgnoreFields(drivelist.Device{}, "Attribs")

func TestCollectFixture(t *testing.T) {
	root := filepath.Join("testdata", "synthetic")
	c := Fixture(root)
	c.ZFS = fakeZFS

	inv, err := c.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if diff := cmp.Diff(wantSynthetic(c.Sys), inv.Devices, ignoreAttribs); diff != "" {
		t.Errorf("Collect mismatch (-want +got):\n%s", diff)
	}
	if got := inv.ByName("/dev/sdb1"); got == nil || got.DeviceName != "sdb" {
		t.Errorf("ByName(/dev/sdb1) = %v, want sdb", got)
	}
	if got := inv.Degraded(); len(got) != 1 || got[0].DeviceName != "sdd" {
		t.Errorf("Degraded() = %v, want [sdd]", got)
	}
}

func TestCollectMissingFixture(t *testing.T) {
	c := Fixture(t.TempDir())
	if _, err := c.Collect(); err == nil {
		t.Error("Collect on an empty tree: got nil error, want error")
	}
}

// TestCaptureRoundTrip captures from the synthetic fixture into a fresh
// directory and checks that collecting from the copy gives the same
// inventory, which is the property real captures rely on.
func TestCaptureRoundTrip(t *testing.T) {
	src := Fixture(filepath.Join("testdata", "synthetic"))
	dir := t.TempDir()
	if err := src.Capture(dir); err != nil {
		t.Fatalf("Capture: %v", err)
	}

	c := Fixture(dir)
	c.ZFS = fakeZFS
	inv, err := c.Collect()
	if err != nil {
		t.Fatalf("Collect from capture: %v", err)
	}
	want := wantSynthetic(c.Sys)
	for _, d := range want {
		if d.Error != "" {
			d.Error = "udevadm: open " + filepath.Join(dir, "exec", "udevadm info --query=all --name=_dev_sdd") + ": no such file or directory"
		}
	}
	if diff := cmp.Diff(want, inv.Devices, ignoreAttribs); diff != "" {
		t.Errorf("round-trip mismatch (-want +got):\n%s", diff)
	}
}

func TestExecKey(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"udevadm", []string{"info", "--query=all", "--name=/dev/sda"}, "udevadm info --query=all --name=_dev_sda"},
		{"/usr/sbin/zpool", []string{"status", "-P"}, "zpool status -P"},
		{"zpool", nil, "zpool"},
	}
	for _, tc := range tests {
		if got := execKey(tc.name, tc.args); got != tc.want {
			t.Errorf("execKey(%q, %q) = %q, want %q", tc.name, tc.args, got, tc.want)
		}
	}
}

func TestExpanderPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/devices/pci0000:00/0000:00:01.0/host4/port-4:0/expander-4:0/port-4:0:0/end_device-4:0:0/block/sda", "/devices/pci0000:00/0000:00:01.0/host4/port-4:0/expander-4:0"},
		{"/devices/pci0000:00/0000:00:17.0/ata3/host2/target2:0:0/2:0:0:0/block/sdc", ""},
	}
	for _, tc := range tests {
		if got := expanderPath(tc.in); got != tc.want {
			t.Errorf("expanderPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
