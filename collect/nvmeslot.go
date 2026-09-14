package collect

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/scottlaird/drivelist"
)

// annotateNVMeSlots gives NVMe drives a place the way SES gives SAS drives
// one. There is no SES for PCIe-attached drives, but two firmware tables
// say where a PCI device is: the hotplug slot table (/sys/bus/pci/slots),
// which names the slot each address is in and is how a U.2 bay looks, and
// SMBIOS type 9, which names every slot the board vendor cared to describe
// ("M.2_1", "PCIE3", or a bare reference designator) by the root port it
// hangs off. The slot table wins where it has the drive; SMBIOS is the
// fallback, and the usual source for an M.2 slot; failing both, the bay is
// the root port's own address ("0000:00:01.3"), which is as fixed per
// board as a designation and lets a profile name a slot the vendor did
// not describe. Either way the enclosure
// is the chassis (keyed on its DMI serial, described by vendor and
// product) and the bay is the slot's name as the firmware gives it, "9-1"
// for the second lane of a bifurcated slot 9. Bays are only known where a
// drive sits in one, plus the sibling lanes of an occupied hotplug slot
// that have nothing behind them, which is how an empty U.2 bay looks;
// empty add-in card slots cannot be told from empty bays and are left
// alone.
func (c *Collector) annotateNVMeSlots(inv *drivelist.Inventory) error {
	slots := readPCISlots(c.sys())
	smbios := readSMBIOSSlots(c.sys())
	key, model, board := dmiChassis(c.sys())
	if key == "" {
		if len(slots) > 0 || len(smbios) > 0 {
			slog.Warn("pci slots known but the chassis has no usable DMI serial; NVMe drives get no location")
		}
		return nil
	}
	occupied := map[string]bool{}
	for _, d := range inv.Devices {
		if !strings.HasPrefix(d.DeviceName, "nvme") {
			continue
		}
		addr := nvmeController(d)
		slot, ok := slots[pciSlotAddress(addr)]
		if ok {
			occupied[slot] = true
		} else {
			slot, ok = pciIdentity(smbios, addr, c.pciAncestors(d.SysPath, addr))
		}
		if !ok {
			continue
		}
		d.EnclosureBay, d.EnclosureVia, d.EnclosureID, d.EnclosureViaID, d.EnclosureModel, d.EnclosureBoard = slot, "pci", key, key, model, board
	}
	bases := map[string]bool{}
	for slot := range occupied {
		bases[slotBase(slot)] = true
	}
	names := make([]string, 0, len(slots))
	for _, name := range slots {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return sasNameLess("s-"+strings.ReplaceAll(names[i], "-", ":"), "s-"+strings.ReplaceAll(names[j], "-", ":"))
	})
	for _, name := range names {
		if occupied[name] || !bases[slotBase(name)] {
			continue
		}
		var addr string
		for a, n := range slots {
			if n == name {
				addr = a
			}
		}
		if _, err := os.Stat(filepath.Join(c.sys(), "bus", "pci", "devices", addr+".0")); err == nil {
			continue // something else is in it
		}
		inv.Add(&drivelist.Device{EnclosureBay: name, EnclosureVia: "pci", EnclosureID: key, EnclosureViaID: key, EnclosureModel: model, EnclosureBoard: board, Uses: []string{"empty"}})
	}
	return nil
}

// pciIdentity names a drive's place from SMBIOS and the bridges above it
// when no hotplug slot has it. SMBIOS names a slot by its root port; some
// firmware names the device's own address instead, so that is tried
// first. Bridges between the named one and the drive (a carrier card's
// PCIe switch, whose ports each hold an M.2) are appended as their
// device.function, "PCI-E Slot 6/00.0/08.0", so two drives on one card
// come out apart; bus numbers are left out because enumeration can move
// them. A designation the firmware gave to more than one root port (a
// bifurcated slot) is qualified by the port's address. With no SMBIOS
// record at all, the root port's address stands in for the designation.
func pciIdentity(smbios map[string]smbiosSlot, addr string, ancestors []string) (string, bool) {
	chain := append([]string{addr}, ancestors...) // nearest first
	named := -1
	var designation string
	for i, a := range chain {
		if s, found := smbios[a]; found {
			named, designation = i, s.Designation
			break
		}
	}
	if named < 0 {
		if len(ancestors) == 0 {
			return "", false
		}
		named, designation = len(chain)-1, chain[len(chain)-1]
	} else {
		n := 0
		for _, s := range smbios {
			if s.Designation == designation {
				n++
			}
		}
		if n > 1 {
			designation += " @" + chain[named]
		}
	}
	// The bridges below the named one, top down.
	for i := named - 1; i >= 1; i-- {
		designation += "/" + pciDevFn(chain[i])
	}
	return designation, true
}

// pciDevFn is the device.function part of a PCI address.
func pciDevFn(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i+1:]
	}
	return addr
}

// readPCISlots maps each hotplug slot's PCI address (domain:bus:device,
// no function) to the slot's name.
func readPCISlots(sys string) map[string]string {
	entries, err := os.ReadDir(filepath.Join(sys, "bus", "pci", "slots"))
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, e := range entries {
		addr := sysAttr(filepath.Join(sys, "bus", "pci", "slots", e.Name()), "address")
		if addr != "" {
			out[addr] = e.Name()
		}
	}
	return out
}

var pciAddress = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-9a-f]$`)

// nvmeController is the PCI address of the drive's controller: from udev's
// ID_PATH (pci-0000:85:00.0-nvme-1, which also covers namespaces under
// nvme-subsystem, whose sysfs path names no PCI device), else the PCI
// component before "nvme" in the sysfs path.
func nvmeController(d *drivelist.Device) string {
	if p := d.Attribs["ID_PATH"]; strings.HasPrefix(p, "pci-") {
		rest := strings.TrimPrefix(p, "pci-")
		if i := strings.Index(rest, "-nvme"); i > 0 && pciAddress.MatchString(rest[:i]) {
			return rest[:i]
		}
	}
	parts := strings.Split(d.SysPath, "/")
	for i, p := range parts {
		if p == "nvme" && i > 0 && pciAddress.MatchString(parts[i-1]) {
			return parts[i-1]
		}
	}
	return ""
}

// pciSlotAddress drops the function from a PCI address: slots are per
// device.
func pciSlotAddress(addr string) string {
	if i := strings.LastIndex(addr, "."); i > 0 {
		return addr[:i]
	}
	return addr
}

// slotBase is the slot a bifurcated lane belongs to: "9" for "9-1".
func slotBase(name string) string {
	base, _, _ := strings.Cut(name, "-")
	return base
}

// dmiPlaceholders are the strings firmware puts where a serial should be.
var dmiPlaceholders = map[string]bool{
	"": true, "default string": true, "to be filled by o.e.m.": true, "system serial number": true, "not specified": true,
	"none": true, "n/a": true, "unknown": true, "not applicable": true, "0123456789": true, "0000000000": true, "123456789": true,
}

// dmiChassis identifies the chassis from DMI: a key from the first real
// serial among product, chassis and board, the vendor and product name as
// the model, and the board name, which tells models apart when a vendor
// reuses a product name ("Venus Series") across boards.
func dmiChassis(sys string) (key, model, board string) {
	id := filepath.Join(sys, "class", "dmi", "id")
	for _, name := range []string{"product_serial", "chassis_serial", "board_serial"} {
		s := sysAttr(id, name)
		if !dmiPlaceholders[strings.ToLower(s)] {
			key = "dmi:" + s
			break
		}
	}
	model = strings.TrimSpace(sysAttr(id, "sys_vendor") + " " + sysAttr(id, "product_name"))
	board = sysAttr(id, "board_name")
	if dmiPlaceholders[strings.ToLower(board)] {
		board = ""
	}
	return key, model, board
}

// captureNVMeSlots copies what annotateNVMeSlots reads: the slot table,
// the DMI identity, and a marker for each slot address with a device
// behind it.
func (c *Collector) captureNVMeSlots(dir string) error {
	for addr := range readPCISlots(c.sys()) {
		slot := ""
		for a, n := range readPCISlots(c.sys()) {
			if a == addr {
				slot = n
			}
		}
		if err := c.copySys(dir, "/bus/pci/slots/"+slot+"/address"); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(c.sys(), "bus", "pci", "devices", addr+".0")); err == nil {
			if err := c.copySys(dir, "/bus/pci/devices/"+addr+".0/class"); err != nil {
				slog.Debug("capture: slot device", "addr", addr, "err", err)
			}
		}
	}
	for _, name := range []string{"sys_vendor", "product_name", "product_serial", "chassis_serial", "board_serial", "board_name"} {
		if err := c.copySys(dir, "/class/dmi/id/"+name); err != nil {
			slog.Debug("capture: dmi", "attr", name, "err", err) // some need root; some boards lack some
		}
	}
	// SMBIOS slot records, and the PCI tree above each NVMe controller
	// whose namespace lives under nvme-subsystem (its own path names no
	// PCI device), so the SMBIOS match can walk up to the root port.
	entries, _ := filepath.Glob(filepath.Join(c.sys(), "firmware", "dmi", "entries", "9-*", "raw"))
	for _, path := range entries {
		if err := c.copySys(dir, strings.TrimPrefix(path, c.sys())); err != nil {
			slog.Debug("capture: smbios slot", "path", path, "err", err) // needs root
		}
	}
	return nil
}

// captureNVMeTree copies the link from /sys/bus/pci/devices to an NVMe
// controller's device directory, with a marker file so the link
// resolves in the fixture.
func (c *Collector) captureNVMeTree(dir, controller string) error {
	if controller == "" {
		return nil
	}
	link := filepath.Join(c.sys(), "bus", "pci", "devices", controller)
	target, err := os.Readlink(link)
	if err != nil {
		return nil
	}
	real, err := filepath.EvalSymlinks(link)
	if err != nil {
		return nil
	}
	if err := c.copySys(dir, strings.TrimPrefix(real, c.sys())+"/class"); err != nil {
		return err
	}
	dst := filepath.Join(dir, "sys", "bus", "pci", "devices", controller)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	os.Remove(dst)
	return os.Symlink(target, dst)
}
