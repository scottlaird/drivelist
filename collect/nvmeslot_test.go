package collect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/scottlaird/drivelist"
)

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content+"\n"), 0o644)
}

// TestNVMeSlots: mgmt1's two U.2 drives sit in the second lanes of
// bifurcated slots 9 and 10; the first lanes have nothing behind them and
// come out as empty bays; the onboard M.2 boot drive is in no slot.
func TestNVMeSlots(t *testing.T) {
	inv, err := Fixture(filepath.Join("testdata", "mgmt1")).Collect()
	if err != nil {
		t.Fatal(err)
	}
	const key = "dmi:KCS0GX0000TB"
	byName := map[string]*drivelist.Device{}
	var empties []*drivelist.Device
	for _, d := range inv.Devices {
		if d.IsEmptyBay() {
			empties = append(empties, d)
			continue
		}
		byName[d.DeviceName] = d
	}
	for name, bay := range map[string]string{"nvme0n1": "9-1", "nvme1n1": "10-1"} {
		d := byName[name]
		if d == nil || d.EnclosureBay != bay || d.EnclosureVia != "pci" || d.EnclosureID != key || d.EnclosureViaID != key || d.EnclosureModel != "ASUSTeK COMPUTER INC. RS500A-E10-RS12U" {
			t.Errorf("%s = %+v, want bay %s in %s", name, d, bay, key)
		}
	}
	// The onboard M.2 is in no hotplug slot and no SMBIOS record names its
	// bridge, so the bridge's address stands in.
	if d := byName["nvme2n1"]; d == nil || d.EnclosureBay != "0000:40:01.2" || d.EnclosureID != key {
		t.Errorf("onboard nvme2n1 = %+v, want bay 0000:40:01.2", d)
	}
	if len(empties) != 2 || empties[0].EnclosureBay != "9" || empties[1].EnclosureBay != "10" || empties[0].EnclosureID != key || empties[0].Uses[0] != "empty" {
		t.Errorf("empty bays = %+v", empties)
	}
}

func TestDMIChassisPlaceholders(t *testing.T) {
	dir := t.TempDir()
	id := filepath.Join(dir, "class", "dmi", "id")
	writeAttr := func(name, v string) {
		t.Helper()
		if err := writeFile(filepath.Join(id, name), v); err != nil {
			t.Fatal(err)
		}
	}
	writeAttr("product_serial", "To be filled by O.E.M.")
	writeAttr("chassis_serial", "0123456789")
	writeAttr("sys_vendor", "Acme")
	writeAttr("product_name", "Box")
	if key, model, board := dmiChassis(dir); key != "" || model != "Acme Box" || board != "" {
		t.Errorf("all placeholders: key %q model %q board %q", key, model, board)
	}
	writeAttr("board_serial", "BRD-42")
	writeAttr("board_name", "AHWSA")
	if key, _, board := dmiChassis(dir); key != "dmi:BRD-42" || board != "AHWSA" {
		t.Errorf("board serial fallback: %q board %q", key, board)
	}
	if nvmeController(&drivelist.Device{Attribs: map[string]string{"ID_PATH": "pci-0000:85:00.0-nvme-1"}}) != "0000:85:00.0" {
		t.Error("ID_PATH not parsed")
	}
	if nvmeController(&drivelist.Device{SysPath: "x/0000:02:00.0/nvme/nvme0/nvme0n1"}) != "0000:02:00.0" {
		t.Error("sysfs path not parsed")
	}
}
