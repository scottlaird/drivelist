package drivelist

import (
	"io/ioutil"
	"strings"
)

type Disks struct {
	Devices []*DiskDevice

	devNames map[string]int
}

func GetDisks() (*Disks, error) {
	d := &Disks{}
	d.Devices = make([]*DiskDevice, 0)
	d.devNames = make(map[string]int)

	names, err := GetDiskNames()
	if err != nil {
		return d, err
	}
	for i, name := range names {
		dev, err := NewDiskDevice("/dev/" + name)
		if err != nil {
			return d, err
		}
		d.Devices = append(d.Devices, dev)
		for _, devName := range d.Devices[i].Devices {
			d.devNames[devName] = i
		}
	}

	return d, nil
}

func (d *Disks) GetDiskByName(name string) *DiskDevice {
	index, ok := d.devNames[name]
	if ok {
		return d.Devices[index]
	}

	// Not found.  But we could be looking at a partitioned device
	// (/dev/sda1, or /dev/disks/by-id/foo-part1, so let's strip
	// those and try again.

	name = strings.TrimRight(name, "0123456789") // remove trailing partition number, if it's there.
	name = strings.TrimSuffix(name, "-part")

	index, ok = d.devNames[name]
	if ok {
		return d.Devices[index]
	}
	return nil
}

func GetDiskNames() ([]string, error) {
	disks := []string{}

	files, err := ioutil.ReadDir("/sys/block/")
	if err != nil {
		return disks, err
	}
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "sd") {
			disks = append(disks, file.Name())
		}
	}

	return disks, nil
}
