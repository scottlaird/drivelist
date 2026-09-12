package collect

import (
	"os"
	"strings"

	"github.com/scottlaird/drivelist"
)

// disks enumerates the sd* block devices in sysfs and identifies each.
func (c *Collector) disks() (*drivelist.Inventory, error) {
	inv := drivelist.NewInventory()
	names, err := c.diskNames()
	if err != nil {
		return inv, err
	}
	for _, name := range names {
		dev, err := c.newDevice("/dev/" + name)
		if err != nil {
			return inv, err
		}
		inv.Add(dev)
	}
	return inv, nil
}

func (c *Collector) diskNames() ([]string, error) {
	var names []string
	entries, err := os.ReadDir(c.sys() + "/block/")
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
