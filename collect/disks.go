package collect

import (
	"os"
	"strings"

	"github.com/scottlaird/drivelist"
)

// disks enumerates the sd* block devices in /sys/block and identifies each.
func disks() (*drivelist.Inventory, error) {
	inv := drivelist.NewInventory()
	names, err := diskNames()
	if err != nil {
		return inv, err
	}
	for _, name := range names {
		dev, err := newDevice("/dev/" + name)
		if err != nil {
			return inv, err
		}
		inv.Add(dev)
	}
	return inv, nil
}

func diskNames() ([]string, error) {
	var names []string
	entries, err := os.ReadDir("/sys/block/")
	if err != nil {
		return names, err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "sd") {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
