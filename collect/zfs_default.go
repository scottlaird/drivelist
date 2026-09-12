//go:build !libzfs

package collect

import "github.com/scottlaird/drivelist"

// annotateZFS runs zpool. Build with -tags libzfs on Linux to use the
// library instead.
func (c *Collector) annotateZFS(inv *drivelist.Inventory) error {
	return c.annotateZFSExec(inv)
}
