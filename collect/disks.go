package collect

import (
	"log/slog"
	"os"
	"strings"

	"github.com/scottlaird/drivelist"
)

// disks enumerates the block devices in sysfs and identifies each. A
// device that cannot be identified is still listed, with Error set, so one
// unresponsive drive does not hide the rest.
func (c *Collector) disks() (*drivelist.Inventory, error) {
	inv := drivelist.NewInventory()
	names, err := c.diskNames()
	if err != nil {
		return inv, err
	}
	for _, name := range names {
		dev, err := c.newDevice("/dev/" + name)
		if err != nil {
			slog.Warn("identifying device", "device", name, "err", err)
			dev = &drivelist.Device{
				DeviceName: name,
				Devices:    []string{"/dev/" + name},
				Uses:       []string{},
				Error:      err.Error(),
			}
		}
		inv.Add(dev)
	}
	return inv, nil
}

// IsDiskName reports whether a kernel block device name is a whole disk
// drivelist handles: SCSI-like (sd*) or an NVMe namespace (nvmeXnY), not a
// partition (sda1, nvme0n1p2). With native NVMe multipathing the kernel
// also lists one hidden nvmeXcYnZ entry per controller path; those have no
// device node and are skipped.
func IsDiskName(name string) bool {
	if rest, ok := strings.CutPrefix(name, "sd"); ok {
		return rest != "" && strings.TrimLeft(rest, "abcdefghijklmnopqrstuvwxyz") == ""
	}
	rest, ok := strings.CutPrefix(name, "nvme")
	if !ok {
		return false
	}
	ctrl, ns, ok := strings.Cut(rest, "n")
	return ok && allDigits(ctrl) && allDigits(ns)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func (c *Collector) diskNames() ([]string, error) {
	var names []string
	entries, err := os.ReadDir(c.sys() + "/block/")
	if err != nil {
		return names, err
	}
	for _, e := range entries {
		if IsDiskName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
