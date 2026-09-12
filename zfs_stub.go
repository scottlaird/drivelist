//go:build !linux || !cgo || nolibzfs

package drivelist

import "log/slog"

// AnnotateDisksZFS is a no-op in builds without libzfs support. ZFS pool
// membership is only detected on Linux builds with cgo and libzfs available;
// build with -tags nolibzfs to get this stub on Linux.
func AnnotateDisksZFS(disks *Disks) error {
	slog.Warn("ZFS support not compiled in; pool membership will not be reported")
	return nil
}
