package collect

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// smbiosSlot is one SMBIOS type 9 (System Slot) record: what the board
// vendor calls a slot and the PCI address its firmware attached to it,
// which for a PCI Express slot is the root port the slot hangs off, not
// whatever is plugged in.
type smbiosSlot struct {
	Designation string // "PCIE3", "M.2_1", "J3502"
	Address     string // "0000:00:01.2"; "" when the record predates SMBIOS 2.6 or is not PCI
	InUse       bool   // current usage 4; 3 is available
}

// readSMBIOSSlots parses the type 9 records the kernel exposes under
// /sys/firmware/dmi/entries (readable as root). Records whose address
// names no device, which firmware does produce, are kept; nothing will
// match them.
func readSMBIOSSlots(sys string) map[string]smbiosSlot {
	entries, err := filepath.Glob(filepath.Join(sys, "firmware", "dmi", "entries", "9-*", "raw"))
	if err != nil || len(entries) == 0 {
		return nil
	}
	sort.Strings(entries)
	out := map[string]smbiosSlot{}
	for _, path := range entries {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		s, ok := parseSMBIOSSlot(raw)
		if ok && s.Address != "" {
			out[s.Address] = s
		}
	}
	return out
}

// parseSMBIOSSlot decodes one type 9 structure: a formatted area whose
// length is byte 1, then the string table (NUL-terminated strings, ended
// by an empty one). Designation is string index byte 4; segment, bus and
// device/function are at 13, 15 and 16 in SMBIOS 2.6 and later.
func parseSMBIOSSlot(raw []byte) (smbiosSlot, bool) {
	if len(raw) < 4 || raw[0] != 9 {
		return smbiosSlot{}, false
	}
	length := int(raw[1])
	if length > len(raw) || length < 12 {
		return smbiosSlot{}, false
	}
	var strs []string
	for rest := raw[length:]; len(rest) > 0; {
		i := 0
		for i < len(rest) && rest[i] != 0 {
			i++
		}
		if i == 0 {
			break
		}
		strs = append(strs, string(rest[:i]))
		if i+1 > len(rest) {
			break
		}
		rest = rest[i+1:]
	}
	s := smbiosSlot{InUse: raw[7] == 4}
	if idx := int(raw[4]); idx > 0 && idx <= len(strs) {
		s.Designation = strings.TrimSpace(strs[idx-1])
	}
	if length >= 17 {
		seg := binary.LittleEndian.Uint16(raw[13:15])
		bus, devfn := raw[15], raw[16]
		s.Address = fmt.Sprintf("%04x:%02x:%02x.%d", seg, bus, devfn>>3, devfn&7)
	}
	return s, s.Designation != ""
}

// pciAncestors are the PCI addresses above a device, nearest first: from
// the device's own sysfs path when it names them, else by resolving the
// controller's link under /sys/bus/pci/devices, which a namespace under
// nvme-subsystem needs.
func (c *Collector) pciAncestors(sysPath, controller string) []string {
	var chain []string
	for _, p := range strings.Split(sysPath, "/") {
		if pciAddress.MatchString(p) {
			chain = append(chain, p)
		}
	}
	if len(chain) == 0 && controller != "" {
		if target, err := filepath.EvalSymlinks(filepath.Join(c.sys(), "bus", "pci", "devices", controller)); err == nil {
			for _, p := range strings.Split(target, "/") {
				if pciAddress.MatchString(p) {
					chain = append(chain, p)
				}
			}
		}
	}
	// Nearest first, and never the device itself.
	var out []string
	for i := len(chain) - 1; i >= 0; i-- {
		if chain[i] != controller {
			out = append(out, chain[i])
		}
	}
	return out
}
