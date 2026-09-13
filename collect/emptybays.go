package collect

import (
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/scottlaird/drivelist"
)

// annotateEmptyBays appends a nameless device for every enclosure bay that
// has a bay_identifier in sysfs but no disk in the inventory. It only looks
// under SAS nodes (expanders, or an HBA with drives on its own ports) that
// already have at least one known disk.
//
// This does not work quite right on every SAS enclosure, so its output is
// best treated as a hint.
// isExpanderOwnBay reports whether path is the expander's own bay_identifier
// (…/sas_device/expander-N:M/bay_identifier) rather than an end device's.
// The expander has one, at least in Linux 5.15, but reading it returns an
// I/O error and it is not a drive bay.
func isExpanderOwnBay(path string) bool {
	return strings.HasPrefix(filepath.Base(filepath.Dir(path)), "expander-")
}

func (c *Collector) annotateEmptyBays(inv *drivelist.Inventory) error {
	owners := make(map[string]bool) // sysfs path of each node with a drive in one of its bays
	usedBays := make(map[string]bool)
	for _, d := range inv.Devices {
		if d.EnclosureVia != "" && d.EnclosureBay != "" {
			owners[viaPath(d)] = true
			usedBays[d.EnclosureVia+" "+d.EnclosureBay] = true
		}
	}

	// Walk owners in a fixed order so the inventory is deterministic. A bay
	// belongs to the nearest expander above it, or to the HBA when there
	// is none, so an expander cascaded behind another (an empty rear
	// backplane) gets its own bays even though nothing sits in them.
	for _, ownerPath := range slices.Sorted(maps.Keys(owners)) {
		err := filepath.WalkDir(ownerPath, func(path string, _ fs.DirEntry, err error) error {
			if err != nil || filepath.Base(path) != "bay_identifier" || isExpanderOwnBay(path) {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				slog.Warn("reading bay_identifier", "path", path, "err", err)
				return nil
			}
			bay := strings.TrimSuffix(string(b), "\n")
			via, viaDir := nearestNode(path)
			if via == "" || usedBays[via+" "+bay] {
				return nil
			}
			usedBays[via+" "+bay] = true
			inv.Add(newEmptyBayDevice(via, viaDir, filepath.Dir(path), bay))
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// viaPath is the sysfs directory of the node a device's enclosure is
// reached through: its expander, or its SCSI host.
func viaPath(d *drivelist.Device) string {
	if d.ExpanderPath != "" {
		return d.ExpanderPath
	}
	var prefix string
	for i, p := range strings.Split(d.SysPath, "/") {
		if i > 0 {
			prefix += "/"
		}
		prefix += p
		if strings.HasPrefix(p, "host") {
			return prefix
		}
	}
	return ""
}

// nearestNode is the SAS node a bay_identifier path hangs off: the last
// expander component, or the SCSI host if there is none, with its
// directory.
func nearestNode(path string) (name, dir string) {
	var prefix string
	for i, p := range strings.Split(path, "/") {
		if i > 0 {
			prefix += "/"
		}
		prefix += p
		switch {
		case strings.HasPrefix(p, "expander-"):
			name, dir = p, prefix
		case strings.HasPrefix(p, "host") && name == "":
			name, dir = p, prefix
		}
	}
	return name, dir
}
