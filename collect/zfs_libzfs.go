//go:build linux && cgo && !nolibzfs

package collect

import (
	"fmt"
	"log/slog"

	zfs "github.com/bicomsystems/go-libzfs"
	"github.com/scottlaird/drivelist"
)

// annotateZFS marks every disk that belongs to an imported pool with its
// position in the pool's vdev tree, as "zfs > <pool> <guid> > … > disk <guid>".
func annotateZFS(inv *drivelist.Inventory) error {
	pools, err := zfs.PoolOpenAll()
	if err != nil {
		return err
	}
	for _, pool := range pools {
		m, _ := devicesFromPool(pool)
		for dev, prefix := range m {
			disk := inv.ByName(dev)
			if disk != nil {
				disk.Uses = append(disk.Uses, prefix)
			} else {
				poolname, _ := pool.Name()
				slog.Error("ZFS pool references an unknown disk; perhaps a drive failed completely or was removed", "pool", poolname, "disk", dev)
			}
		}
	}
	return nil
}

func devicesFromPool(pool zfs.Pool) (map[string]string, error) {
	m := make(map[string]string)
	v, err := pool.VDevTree()
	if err != nil {
		return m, err
	}
	addDevicesFromVDevTree(&v, m, "zfs")
	return m, nil
}

func addDevicesFromVDevTree(v *zfs.VDevTree, m map[string]string, prefix string) {
	guid := fmt.Sprintf("%d", v.GUID)

	if v.Type == "disk" {
		m[v.Name] = prefix + " > disk " + guid
	}
	for _, vv := range v.Devices {
		addDevicesFromVDevTree(&vv, m, prefix+" > "+v.Name+" "+guid)
	}
	if v.Logs != nil {
		addDevicesFromVDevTree(v.Logs, m, prefix+" > "+v.Name+" "+guid+" > log")
	}
	for _, vv := range v.L2Cache {
		addDevicesFromVDevTree(&vv, m, prefix+" > l2arc "+v.Name)
	}
	for _, vv := range v.Spares {
		addDevicesFromVDevTree(&vv, m, prefix+" > spare "+v.Name)
	}
}
