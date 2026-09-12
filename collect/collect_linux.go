//go:build linux

package collect

import "github.com/scottlaird/drivelist"

// All enumerates every disk on this host and annotates each with its uses
// (ZFS pool membership, mounts) and, where the enclosure reports it, its
// physical bay. Empty enclosure bays are appended as devices with no name.
func All() (*drivelist.Inventory, error) {
	inv, err := disks()
	if err != nil {
		return inv, err
	}
	for _, annotate := range []func(*drivelist.Inventory) error{
		annotateZFS,
		annotateMounts,
		annotateMD,
		annotateLVM,
		annotateEmptyBays,
	} {
		if err := annotate(inv); err != nil {
			return inv, err
		}
	}
	return inv, nil
}
