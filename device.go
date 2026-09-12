// Package drivelist describes the drives in a system: what they are, how to
// find them physically, and what they are used for. Collection from a live
// system lives in the collect subpackage; this package holds the types and
// the platform-independent helpers so that they build everywhere.
package drivelist

import "strings"

// Device is one block device, or an empty enclosure bay (see IsEmptyBay).
type Device struct {
	DeviceName string   // kernel name, e.g. "sda"
	Devices    []string // every name in /dev that refers to this disk
	WWN        string
	SysPath    string
	Model      string
	Serial     string
	Attribs    map[string]string // raw udev properties
	Uses       []string          // what the disk is used for; empty means unused
	// GenericDevice is the SCSI generic (bsg) node for the SAS end device.
	GenericDevice string

	// Enclosure data.
	Expander     string
	ExpanderPath string
	EnclosureBay string

	Size uint64 // bytes
}

// IsEmptyBay reports whether this entry is an enclosure bay with no drive
// in it rather than a device.
func (d *Device) IsEmptyBay() bool {
	return d.DeviceName == "" && d.EnclosureBay != ""
}

// Unused reports whether no use has been found for the device.
func (d *Device) Unused() bool {
	return len(d.Uses) == 0
}

// Inventory is the set of devices found on one host, indexed by every
// name they are known under.
type Inventory struct {
	Devices []*Device

	byName map[string]int
}

// NewInventory returns an empty inventory.
func NewInventory() *Inventory {
	return &Inventory{byName: make(map[string]int)}
}

// Add appends d and indexes each of its names for ByName.
func (i *Inventory) Add(d *Device) {
	if i.byName == nil {
		i.byName = make(map[string]int)
	}
	i.Devices = append(i.Devices, d)
	for _, name := range d.Devices {
		i.byName[name] = len(i.Devices) - 1
	}
}

// ByName finds a device by any of its /dev names. A partition name
// (/dev/sda1, /dev/disk/by-id/foo-part1) resolves to its parent disk. It
// returns nil when nothing matches.
func (i *Inventory) ByName(name string) *Device {
	if idx, ok := i.byName[name]; ok {
		return i.Devices[idx]
	}
	name = strings.TrimRight(name, "0123456789")
	name = strings.TrimSuffix(name, "-part")
	if idx, ok := i.byName[name]; ok {
		return i.Devices[idx]
	}
	return nil
}
