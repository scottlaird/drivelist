package drivelist

import "testing"

func TestInventoryByName(t *testing.T) {
	inv := NewInventory()
	sda := &Device{DeviceName: "sda", Devices: []string{"/dev/sda", "/dev/disk/by-id/wwn-0x1"}}
	sdb := &Device{DeviceName: "sdb", Devices: []string{"/dev/sdb"}}
	inv.Add(sda)
	inv.Add(sdb)

	tests := []struct {
		name string
		want *Device
	}{
		{"/dev/sda", sda},
		{"/dev/sda1", sda},
		{"/dev/disk/by-id/wwn-0x1", sda},
		{"/dev/disk/by-id/wwn-0x1-part3", sda},
		{"/dev/sdb", sdb},
		{"/dev/sdc", nil},
		{"space", nil},
	}
	for _, tc := range tests {
		if got := inv.ByName(tc.name); got != tc.want {
			t.Errorf("ByName(%q) = %v, want %v", tc.name, got, tc.want)
		}
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
