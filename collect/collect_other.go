//go:build !linux

package collect

import "github.com/scottlaird/drivelist"

// All returns ErrUnsupported: enumeration depends on Linux sysfs and udev.
func All() (*drivelist.Inventory, error) {
	return nil, ErrUnsupported
}
