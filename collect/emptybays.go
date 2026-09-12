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
func annotateEmptyBays(inv *drivelist.Inventory) error {
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
			// The expander itself has a bay_identifier (at least in Linux
			// 5.15), but reading it returns an I/O error; skip it.
			if ok, _ := filepath.Match("*/expander-*/bay_identifier", path); ok {
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
