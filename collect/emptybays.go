package collect

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/scottlaird/drivelist"
)

// annotateEmptyBays appends a nameless device for every enclosure bay that
// has a bay_identifier in sysfs but no disk in the inventory. It only looks
// under expanders that already have at least one known disk.
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
	expanders := make(map[string]bool)
	usedBays := make(map[string]bool)
	for _, d := range inv.Devices {
		if d.Expander != "" {
			expanders[d.ExpanderPath] = true
			usedBays[d.Expander+" "+d.EnclosureBay] = true
		}
	}

	for expanderPath := range expanders {
		expander := filepath.Base(expanderPath)
		err := filepath.WalkDir(expanderPath, func(path string, _ fs.DirEntry, err error) error {
			if err != nil || filepath.Base(path) != "bay_identifier" {
				return nil
			}
			if isExpanderOwnBay(path) {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				slog.Warn("reading bay_identifier", "path", path, "err", err)
				return nil
			}
			bay := strings.TrimSuffix(string(b), "\n")
			if !usedBays[expander+" "+bay] {
				inv.Add(newEmptyBayDevice(expander, expanderPath, bay))
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
