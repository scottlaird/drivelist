package collect

import (
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/scottlaird/drivelist"
)

// newDevice identifies one block device through udev and locates it through
// the SAS topology in sysfs.
func newDevice(name string) (*drivelist.Device, error) {
	u, err := udevInfo(name)
	if err != nil {
		return nil, err
	}
	d := &drivelist.Device{
		DeviceName: u.DeviceName,
		Attribs:    u.Attribs,
		Uses:       []string{},
	}
	d.SysPath = "/sys" + d.Attribs["DEVPATH"]
	d.WWN = d.Attribs["ID_WWN"]
	d.Model = d.Attribs["ID_MODEL"]
	d.Serial = d.Attribs["SCSI_IDENT_SERIAL"]

	d.Devices = []string{"/dev/" + d.DeviceName}
	for _, link := range strings.Split(d.Attribs["DEVLINKS"], " ") {
		d.Devices = append(d.Devices, link)
	}

	populateSES(d)

	sizeString, err := os.ReadFile("/sys/class/block/" + d.DeviceName + "/size")
	if err != nil {
		slog.Error("reading disk size", "device", d.DeviceName, "err", err)
	} else {
		sizeBlocks, _ := strconv.ParseUint(strings.TrimSuffix(string(sizeString), "\n"), 10, 64)
		d.Size = sizeBlocks * 512
	}
	return d, nil
}

// populateSES walks the device's sysfs path for the SAS expander and end
// device it hangs off, and reads the enclosure bay number if there is one.
func populateSES(d *drivelist.Device) {
	var prefix, endDevice, endDevicePath string

	for _, p := range strings.Split(d.SysPath, "/") {
		prefix += "/" + p
		if strings.HasPrefix(p, "expander-") {
			d.Expander = p
			d.ExpanderPath = prefix
		}
		if strings.HasPrefix(p, "end_device-") {
			endDevice = p
			endDevicePath = prefix
			d.GenericDevice = "/dev/bsg/" + endDevice
		}
	}
	if endDevice != "" {
		bay, err := os.ReadFile(endDevicePath + "/sas_device/" + endDevice + "/bay_identifier")
		if err == nil {
			d.EnclosureBay = strings.TrimSuffix(string(bay), "\n")
		}
	}
}

// newEmptyBayDevice describes an enclosure bay with nothing in it.
func newEmptyBayDevice(expander, expanderPath, bay string) *drivelist.Device {
	return &drivelist.Device{
		Expander:     expander,
		ExpanderPath: expanderPath,
		EnclosureBay: bay,
		Uses:         []string{"empty"},
	}
}
