package collect

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/scottlaird/drivelist"
)

// The macOS collector. diskutil enumerates whole disks with their size,
// bus and partitions; system_profiler's NVMe, SATA and USB reports carry
// the model and serial number, which diskutil does not; mounted partitions
// and APFS volumes become "mount > /Volumes/…" uses. There are no WWNs on
// macOS, so identity is model plus serial. Enclosure bays do not apply.
//
// It is selected by Collector.Platform (runtime.GOOS by default), and
// every input comes through Exec, so a capture from a Mac collects on any
// platform.

var profilerTypes = []string{"SPNVMeDataType", "SPSerialATADataType", "SPUSBDataType"}

func (c *Collector) collectDarwin() (*drivelist.Inventory, error) {
	inv := drivelist.NewInventory()

	out, err := c.run("diskutil", "list", "-plist", "physical")
	if err != nil {
		return inv, fmt.Errorf("diskutil list: %w", err)
	}
	listed, err := parsePlist(bytes.NewReader(out))
	if err != nil {
		return inv, fmt.Errorf("diskutil list: %w", err)
	}
	top, _ := listed.(map[string]any)
	var wholeDisks []string
	for _, v := range dictAny(top, "WholeDisks") {
		if s, ok := v.(string); ok {
			wholeDisks = append(wholeDisks, s)
		}
	}
	sort.Slice(wholeDisks, func(i, j int) bool { return diskNumber(wholeDisks[i]) < diskNumber(wholeDisks[j]) })

	ids := c.profilerIdentities()
	mounts, err := c.darwinMounts()
	if err != nil {
		return inv, err
	}

	for _, name := range wholeDisks {
		d, err := c.darwinDevice(name, ids[name], mounts[name])
		if err != nil {
			d = &drivelist.Device{DeviceName: name, Devices: []string{"/dev/" + name}, Uses: []string{}, Error: err.Error()}
		}
		inv.Add(d)
	}
	if err := c.zfsAnnotator()(inv); err != nil {
		return inv, err
	}
	return inv, nil
}

// darwinDevice describes one whole disk from diskutil info plus the
// profiler's identity for it.
func (c *Collector) darwinDevice(name string, id profilerIdentity, uses []string) (*drivelist.Device, error) {
	out, err := c.run("diskutil", "info", "-plist", name)
	if err != nil {
		return nil, fmt.Errorf("diskutil info: %w", err)
	}
	v, err := parsePlist(bytes.NewReader(out))
	if err != nil {
		return nil, fmt.Errorf("diskutil info: %w", err)
	}
	info, _ := v.(map[string]any)
	d := &drivelist.Device{
		DeviceName: name,
		Devices:    []string{"/dev/" + name},
		Model:      id.model,
		Serial:     id.serial,
		SysPath:    dictString(info, "DeviceTreePath"),
		Size:       uint64(dictInt(info, "Size")),
		Uses:       append([]string{}, uses...),
		Attribs:    map[string]string{},
	}
	if d.Model == "" {
		d.Model = strings.TrimSpace(dictString(info, "MediaName"))
	}
	protocol := dictString(info, "BusProtocol")
	d.Attribs["BUS_PROTOCOL"] = protocol
	d.Attribs["ID_MODEL"] = d.Model
	if d.Serial != "" {
		d.Attribs["ID_SERIAL_SHORT"] = d.Serial
	}
	switch {
	case strings.Contains(protocol, "USB"):
		d.Attribs["ID_BUS"] = "usb"
	case strings.Contains(protocol, "SATA"):
		d.Attribs["ID_BUS"] = "ata"
	case strings.Contains(protocol, "PCI"), strings.Contains(protocol, "Apple Fabric"), id.nvme:
		d.Attribs["ID_BUS"] = "nvme"
	}
	if dictBool(info, "SolidState") {
		d.Attribs["ID_ATA_ROTATION_RATE_RPM"] = "0"
	}
	// A disk none of the reports give a serial for (some enclosures and
	// RAID boxes) is recorded without identity rather than as an error: it
	// is not a failure to read, there is nothing to read.
	sort.Strings(d.Uses)
	d.Uses = slices.Compact(d.Uses)
	return d, nil
}

type profilerIdentity struct {
	model, serial string
	nvme          bool
}

// profilerIdentities runs the three storage reports and maps each BSD
// device name to the model and serial the report gives it. Failures are
// tolerated: a report that is absent or unparsable just contributes
// nothing.
func (c *Collector) profilerIdentities() map[string]profilerIdentity {
	ids := map[string]profilerIdentity{}
	for _, typ := range profilerTypes {
		out, err := c.run("system_profiler", typ, "-json")
		if err != nil {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(out, &doc); err != nil {
			continue
		}
		walkProfiler(doc, typ == "SPNVMeDataType", "", "", ids)
	}
	return ids
}

// walkProfiler descends the report. NVMe and SATA entries carry bsd_name,
// device_model and device_serial on the same dict; USB devices carry
// serial_num and _name on the device and bsd_name on the Media entries
// below it, so those inherit from the enclosing device.
func walkProfiler(v any, nvme bool, model, serial string, ids map[string]profilerIdentity) {
	switch x := v.(type) {
	case map[string]any:
		if s, _ := x["serial_num"].(string); s != "" {
			serial = s
			if n, _ := x["_name"].(string); n != "" {
				model = n
			}
		}
		if s, _ := x["device_serial"].(string); s != "" {
			serial = s
			if m, _ := x["device_model"].(string); m != "" {
				model = m
			}
		}
		if bsd, _ := x["bsd_name"].(string); bsd != "" && serial != "" && IsWholeDiskName(bsd) {
			ids[bsd] = profilerIdentity{model: strings.TrimSpace(model), serial: strings.TrimSpace(serial), nvme: nvme}
		}
		for _, child := range x {
			walkProfiler(child, nvme, model, serial, ids)
		}
	case []any:
		for _, item := range x {
			walkProfiler(item, nvme, model, serial, ids)
		}
	}
}

// darwinMounts maps each whole disk to its "mount > path" uses: its own
// mounted partitions, and the mounted volumes of any APFS container whose
// physical store is one of its partitions.
func (c *Collector) darwinMounts() (map[string][]string, error) {
	out, err := c.run("diskutil", "list", "-plist")
	if err != nil {
		return nil, fmt.Errorf("diskutil list: %w", err)
	}
	v, err := parsePlist(bytes.NewReader(out))
	if err != nil {
		return nil, fmt.Errorf("diskutil list: %w", err)
	}
	top, _ := v.(map[string]any)
	mounts := map[string][]string{}
	add := func(disk, mp string) {
		if disk != "" && mp != "" {
			mounts[disk] = append(mounts[disk], "mount > "+mp)
		}
	}
	for _, entry := range dictList(top, "AllDisksAndPartitions") {
		id := dictString(entry, "DeviceIdentifier")
		// Physical disks: their partitions' mount points.
		for _, p := range dictList(entry, "Partitions") {
			add(id, dictString(p, "MountPoint"))
		}
		// Synthesized APFS containers: volumes belong to the physical
		// store's disk.
		stores := dictList(entry, "APFSPhysicalStores")
		if len(stores) == 0 {
			continue
		}
		for _, store := range stores {
			phys := wholeDiskOf(dictString(store, "DeviceIdentifier"))
			for _, vol := range dictList(entry, "APFSVolumes") {
				add(phys, dictString(vol, "MountPoint"))
				for _, snap := range dictList(vol, "MountedSnapshots") {
					add(phys, dictString(snap, "SnapshotMountPoint"))
				}
			}
		}
	}
	return mounts, nil
}

// IsWholeDiskName reports whether a BSD name is a whole disk (disk3) rather
// than a slice (disk3s2).
func IsWholeDiskName(name string) bool {
	rest, ok := strings.CutPrefix(name, "disk")
	return ok && rest != "" && strings.TrimLeft(rest, "0123456789") == ""
}

// wholeDiskOf strips a slice suffix: disk0s2 -> disk0.
func wholeDiskOf(name string) string {
	rest, ok := strings.CutPrefix(name, "disk")
	if !ok {
		return name
	}
	digits := rest[:len(rest)-len(strings.TrimLeft(rest, "0123456789"))]
	return "disk" + digits
}

func diskNumber(name string) int {
	n := 0
	for _, r := range strings.TrimPrefix(name, "disk") {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func dictAny(m map[string]any, key string) []any {
	items, _ := m[key].([]any)
	return items
}
