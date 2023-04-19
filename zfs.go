package drivelist

import (
	"fmt"

	zfs "github.com/bicomsystems/go-libzfs"
	"github.com/golang/glog"
)

func GetDevicesFromZfsPool(pool zfs.Pool) (map[string]string, error) {
	m := make(map[string]string)
	v, err := pool.VDevTree()
	if err != nil {
		return m, err
	}
	AddDevicesFromZfsVDevTree(&v, m, "zfs")
	return m, err
}

func AddDevicesFromZfsVDevTree(v *zfs.VDevTree, m map[string]string, prefix string) {
	guid := fmt.Sprintf("%d", v.GUID)

	if v.Type == "disk" {
		m[v.Name] = prefix + " > disk " + guid
	}

	if len(v.Devices) > 0 {
		for _, vv := range v.Devices {
			AddDevicesFromZfsVDevTree(&vv, m, prefix+" > "+v.Name+" "+guid)
		}
	}

	if v.Logs != nil {
		AddDevicesFromZfsVDevTree(v.Logs, m, prefix+" > "+v.Name+" "+guid+" > log")
	}

	if len(v.L2Cache) > 0 {
		for _, vv := range v.L2Cache {
			AddDevicesFromZfsVDevTree(&vv, m, prefix+" > l2arc "+v.Name)
		}
	}

	if len(v.Spares) > 0 {
		for _, vv := range v.Spares {
			AddDevicesFromZfsVDevTree(&vv, m, prefix+" > spare "+v.Name)
		}
	}
}

func AnnotateDisksZFS(disks *Disks) error {
	pools, err := zfs.PoolOpenAll()
	if err != nil {
		return err
	}

	for _, pool := range pools {
		m, _ := GetDevicesFromZfsPool(pool)

		for dev, prefix := range m {
			disk := disks.GetDiskByName(dev)
			if disk != nil {
				disk.Uses = append(disk.Uses, prefix)
			} else {
				poolname, _ := pool.Name()
				glog.Errorf("** ZFS Pool %q references an unknown disk (ID %q).  Perhaps a drive failed completely or was removed?", poolname, dev)
			}
		}
	}
	return nil
}
