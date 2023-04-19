package drivelist

import (
	"github.com/golang/glog"
	"github.com/moby/sys/mountinfo"
)

func AnnotateDisksMounts(disks *Disks) error {
	mounts, err := mountinfo.GetMounts(func(i *mountinfo.Info) (skip, stop bool) {
		return false, false
	})
	if err != nil {
		return err
	}

	for _, mount := range mounts {
		glog.Infof("Mount %s -> %s\n", mount.Source, mount.Mountpoint)
		disk := disks.GetDiskByName(mount.Source)
		if disk != nil {
			disk.Uses = append(disk.Uses, "mount > "+mount.Mountpoint)
		}
	}
	return nil
}
