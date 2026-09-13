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
	// MemberState is the ZFS vdev state (ONLINE, DEGRADED, FAULTED, REMOVED,
	// UNAVAIL, AVAIL, INUSE) when the disk belongs to a pool, else "".
	MemberState string
	// GenericDevice is the SCSI generic (bsg) node for the SAS end device.
	GenericDevice string

	// Enclosure data.
	Expander     string // the kernel's name, expander-H:N; H follows probe order
	ExpanderID   string // the expander's SAS address, stable across boots; "" when unknown
	ExpanderPath string
	EnclosureBay string

	Size uint64 // bytes

	// Error is why the device could not be identified, when it could not.
	// Such a device keeps its kernel name and nothing else; a report
	// containing one is degraded rather than wrong.
	Error string
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

// PoolMember is a device a storage pool expects that matched no Device in
// the inventory: a drive that has failed completely, been pulled, or is
// present but invisible to the block layer.
type PoolMember struct {
	Pool  string
	Path  string // as the pool reports it, e.g. /dev/disk/by-id/wwn-0x…-part1
	GUID  string
	State string // UNAVAIL, REMOVED, FAULTED, …
}

// Inventory is the set of devices found on one host, indexed by every
// name they are known under.
type Inventory struct {
	Devices []*Device
	// Unmapped lists pool members with no matching device.
	Unmapped []PoolMember

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
// resolves to its parent disk, whichever convention names it: /dev/sda1,
// /dev/nvme0n1p2, or /dev/disk/by-id/foo-part1. It returns nil when nothing
// matches.
func (i *Inventory) ByName(name string) *Device {
	for _, candidate := range partitionParents(name) {
		if idx, ok := i.byName[candidate]; ok {
			return i.Devices[idx]
		}
	}
	return nil
}

// partitionParents lists the names to try for name, most specific first:
// the name itself, then the disk it would be a partition of.
func partitionParents(name string) []string {
	candidates := []string{name}
	stem := strings.TrimRight(name, "0123456789")
	if stem == name {
		return candidates
	}
	switch {
	case strings.HasSuffix(stem, "-part"):
		candidates = append(candidates, strings.TrimSuffix(stem, "-part"))
	case strings.HasSuffix(stem, "p") && len(stem) > 1 && isDigit(stem[len(stem)-2]):
		// nvme0n1p2, mmcblk0p1: a "p" separates the partition number only
		// when the disk name itself ends in a digit.
		candidates = append(candidates, stem, stem[:len(stem)-1])
	default:
		candidates = append(candidates, stem)
	}
	return candidates
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// Degraded lists the devices that could not be identified.
func (i *Inventory) Degraded() []*Device {
	var out []*Device
	for _, d := range i.Devices {
		if d.Error != "" {
			out = append(out, d)
		}
	}
	return out
}
