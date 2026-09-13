package collect

import (
	"strings"
	"testing"
)

func TestParsePlist(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>WholeDisks</key>
	<array>
		<string>disk0</string>
		<string>disk4</string>
	</array>
	<key>Size</key>
	<integer>1000555581440</integer>
	<key>SolidState</key>
	<true/>
	<key>Removable</key>
	<false/>
	<key>Ratio</key>
	<real>0.5</real>
	<key>Empty</key>
	<array/>
	<key>Nested</key>
	<dict>
		<key>MountPoint</key>
		<string>/Volumes/Scott</string>
	</dict>
</dict>
</plist>`
	v, err := parsePlist(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("top = %T", v)
	}
	if got := dictAny(m, "WholeDisks"); len(got) != 2 || got[1] != "disk4" {
		t.Errorf("WholeDisks = %v", got)
	}
	if dictInt(m, "Size") != 1000555581440 || !dictBool(m, "SolidState") || dictBool(m, "Removable") {
		t.Errorf("scalars = %v", m)
	}
	if r, _ := m["Ratio"].(float64); r != 0.5 {
		t.Errorf("Ratio = %v", m["Ratio"])
	}
	if e, _ := m["Empty"].([]any); e == nil || len(e) != 0 {
		t.Errorf("Empty = %#v", m["Empty"])
	}
	if dictString(m["Nested"].(map[string]any), "MountPoint") != "/Volumes/Scott" {
		t.Errorf("Nested = %v", m["Nested"])
	}
}

func TestWholeDiskNames(t *testing.T) {
	for in, want := range map[string]string{"disk0s2": "disk0", "disk12s1": "disk12", "disk3": "disk3", "sda": "sda"} {
		if got := wholeDiskOf(in); got != want {
			t.Errorf("wholeDiskOf(%q) = %q, want %q", in, got, want)
		}
	}
	for name, want := range map[string]bool{"disk0": true, "disk12": true, "disk0s2": false, "disk": false, "sda": false} {
		if got := IsWholeDiskName(name); got != want {
			t.Errorf("IsWholeDiskName(%q) = %v", name, got)
		}
	}
}

func TestWalkProfiler(t *testing.T) {
	// The shapes system_profiler uses: NVMe/SATA carry the serial beside
	// bsd_name; USB carries it on the device, with Media entries below.
	doc := map[string]any{
		"SPNVMeDataType": []any{map[string]any{
			"_name": "Apple SSD Controller",
			"_items": []any{map[string]any{
				"_name": "APPLE SSD AP1024Z", "bsd_name": "disk0", "device_model": "APPLE SSD AP1024Z", "device_serial": "0ba0",
				"volumes": []any{map[string]any{"_name": "Macintosh HD", "bsd_name": "disk0s2"}},
			}},
		}},
		"SPUSBDataType": []any{map[string]any{
			"_items": []any{map[string]any{
				"_name": "My Passport 2626", "serial_num": "WX12", "manufacturer": "Western Digital",
				"Media": []any{map[string]any{"_name": "My Passport 2626", "bsd_name": "disk12", "volumes": []any{map[string]any{"bsd_name": "disk12s1"}}}},
			}},
		}},
	}
	ids := map[string]profilerIdentity{}
	walkProfiler(doc["SPNVMeDataType"], true, "", "", ids)
	walkProfiler(doc["SPUSBDataType"], false, "", "", ids)
	if got := ids["disk0"]; got.model != "APPLE SSD AP1024Z" || got.serial != "0ba0" || !got.nvme {
		t.Errorf("disk0 = %+v", got)
	}
	if got := ids["disk12"]; got.model != "My Passport 2626" || got.serial != "WX12" || got.nvme {
		t.Errorf("disk12 = %+v", got)
	}
	if _, ok := ids["disk0s2"]; ok {
		t.Error("a slice was recorded as a disk")
	}
}
