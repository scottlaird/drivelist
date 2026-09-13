// Package collect builds a drivelist.Inventory from the running system.
//
// Full collection needs Linux: block devices are enumerated from sysfs,
// identified through udev, and located through the SAS enclosure entries in
// sysfs. macOS gets identity, size, bus and mounts through diskutil and
// system_profiler. Other platforms get ErrUnsupported. The parsers for the
// individual sources are platform-independent and tested everywhere.
package collect

import "errors"

// ErrUnsupported is returned by Collect on platforms where drive collection
// is not implemented.
var ErrUnsupported = errors.New("drive collection is not supported on this platform")
