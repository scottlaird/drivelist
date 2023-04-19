package drivelist

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang/glog"
)

// function AnnotateEmptyBays attempts to build up a list of drive
// bays without drives installed.  This doesn't seem to work quite
// right for any of my current SAS enclosures, but it probably
// *should*.
func AnnotateEmptyBays(disks *Disks) error {
	// Build a list of all expanders currently in use and a list
	// of known (expander, bay) pairs.
	expanders := make(map[string]bool)
	usedBays := make(map[string]bool)

	for _, d := range disks.Devices {
		if len(d.Expander) > 0 {
			expanders[d.ExpanderPath] = true
			usedBays[d.Expander+" "+d.EnclosureBay] = true
		}
	}

	// Underneath each expander /sys directory, look for
	// "bay_identifier" files.  For each, see if it's currently in
	// use.  If not, call NewEmptyBayDiskDevice() and append to
	// disks.
	for e, _ := range expanders {
		err := filepath.WalkDir(e, func(path string, d fs.DirEntry, err error) error {
			if filepath.Base(path) == "bay_identifier" {
				expanderDevice, _ := filepath.Match("*/expander-*/bay_identifier", path)
				if !expanderDevice {
					// The expander itself has a bay_identifier file,
					// at least in Linux 5.15, but reading it returns
					// an I/O error.
					//
					// Since we're only interested in drives, we can
					// just skip it.
					return nil
				}

				b, err := os.ReadFile(path)
				if err != nil {
					glog.Errorf("Failed to read bay_identifier for %q: %v\n", path, err)
					return nil // Ignore errors
				}
				bay := strings.TrimSuffix(string(b), "\n")
				expander := filepath.Base(e)
				newDrive, err := NewEmptyBayDiskDevice(expander, e, bay)

				if usedBays[expander+" "+bay] != true {
					disks.Devices = append(disks.Devices, newDrive)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}

	return nil
}
