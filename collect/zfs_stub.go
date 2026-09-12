//go:build !linux || !cgo || nolibzfs

package collect

import (
	"log/slog"

	"github.com/scottlaird/drivelist"
)

// annotateZFS is a no-op in builds without libzfs. ZFS pool membership is
// only detected on Linux builds with cgo and libzfs available; build with
// -tags nolibzfs to get this stub on Linux.
func annotateZFS(*drivelist.Inventory) error {
	slog.Warn("ZFS support not compiled in; pool membership will not be reported")
	return nil
}
