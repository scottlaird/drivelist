package drivelist

import "testing"

func TestInventoryByName(t *testing.T) {
	inv := NewInventory()
	sda := &Device{DeviceName: "sda", Devices: []string{"/dev/sda", "/dev/disk/by-id/wwn-0x1"}}
	sdb := &Device{DeviceName: "sdb", Devices: []string{"/dev/sdb"}}
	sdp := &Device{DeviceName: "sdp", Devices: []string{"/dev/sdp"}}
	nvme := &Device{DeviceName: "nvme0n1", Devices: []string{"/dev/nvme0n1", "/dev/disk/by-id/nvme-eui.0025385"}}
	for _, d := range []*Device{sda, sdb, sdp, nvme} {
		inv.Add(d)
	}

	tests := []struct {
		name string
		want *Device
	}{
		{"/dev/sda", sda},
		{"/dev/sda1", sda},
		{"/dev/disk/by-id/wwn-0x1", sda},
		{"/dev/disk/by-id/wwn-0x1-part3", sda},
		{"/dev/sdb", sdb},
		{"/dev/sdp", sdp},
		{"/dev/sdp1", sdp},
		{"/dev/nvme0n1", nvme},
		{"/dev/nvme0n1p2", nvme},
		{"/dev/disk/by-id/nvme-eui.0025385-part1", nvme},
		{"/dev/sdc", nil},
		{"/dev/nvme1n1p1", nil},
		{"space", nil},
	}
	for _, tc := range tests {
		if got := inv.ByName(tc.name); got != tc.want {
			t.Errorf("ByName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestInventoryDegraded(t *testing.T) {
	inv := NewInventory()
	inv.Add(&Device{DeviceName: "sda"})
	bad := &Device{DeviceName: "sdb", Error: "udevadm: exit status 1"}
	inv.Add(bad)
	got := inv.Degraded()
	if len(got) != 1 || got[0] != bad {
		t.Errorf("Degraded() = %v, want [sdb]", got)
	}
}

func TestDevicePredicates(t *testing.T) {
	bay := &Device{Expander: "expander-4:0", EnclosureBay: "3"}
	if !bay.IsEmptyBay() {
		t.Error("empty bay entry: IsEmptyBay() = false, want true")
	}
	disk := &Device{DeviceName: "sda", EnclosureBay: "3"}
	if disk.IsEmptyBay() {
		t.Error("disk in a bay: IsEmptyBay() = true, want false")
	}
	if !disk.Unused() {
		t.Error("disk with no uses: Unused() = false, want true")
	}
	disk.Uses = []string{"mount > /"}
	if disk.Unused() {
		t.Error("mounted disk: Unused() = true, want false")
	}
}
