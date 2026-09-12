//go:build linux

package collect

import "github.com/scottlaird/drivelist"

// All collects from the live system with default settings.
func All() (*drivelist.Inventory, error) {
	return (&Collector{}).Collect()
}
