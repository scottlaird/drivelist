package collect

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/scottlaird/drivelist"
)

// newDevice identifies one block device through udev and locates it through
// the SAS topology in sysfs.
func (c *Collector) newDevice(name string) (*drivelist.Device, error) {
	u, err := c.udevInfo(name)
	if err != nil {
		return nil, fmt.Errorf("udevadm: %w", err)
	}
	if u.DeviceName == "" {
		return nil, errors.New("udevadm: no N: line in output")
	}
	d := &drivelist.Device{
		DeviceName: u.DeviceName,
		Attribs:    u.Attribs,
		Uses:       []string{},
	}
	d.SysPath = c.sys() + d.Attribs["DEVPATH"]
	d.WWN = d.Attribs["ID_WWN"]
	d.Model = d.Attribs["ID_MODEL"]
	d.Serial = d.Attribs["SCSI_IDENT_SERIAL"]
	if d.Serial == "" {
		d.Serial = d.Attribs["ID_SERIAL_SHORT"] // NVMe and ATA-over-USB have no SCSI VPD serial
	}

	d.Devices = []string{"/dev/" + d.DeviceName}
	for _, link := range strings.Split(d.Attribs["DEVLINKS"], " ") {
		if link != "" {
			d.Devices = append(d.Devices, link)
		}
	}

	populateSES(d)

	sizeString, err := os.ReadFile(d.SysPath + "/size")
	if err != nil {
		slog.Error("reading disk size", "device", d.DeviceName, "err", err)
	} else {
		sizeBlocks, _ := strconv.ParseUint(strings.TrimSpace(string(sizeString)), 10, 64)
		d.Size = sizeBlocks * 512
	}
	return d, nil
}

// populateSES walks the device's sysfs path for the SAS node the drive
// hangs off and reads the enclosure bay number if there is one. The node
// is the expander when there is one; a drive on the HBA's own ports (the
// server's front panel, say) takes the HBA itself, so its bays can be
// named and tracked like a shelf's.
func populateSES(d *drivelist.Device) {
	var prefix, endDevice, endDevicePath, host, hostPath string

	for i, p := range strings.Split(d.SysPath, "/") {
		if i > 0 {
			prefix += "/"
		}
		prefix += p
		switch {
		case strings.HasPrefix(p, "expander-"):
			d.Expander = p
			d.ExpanderPath = prefix
		case strings.HasPrefix(p, "host") && host == "":
			host, hostPath = p, prefix
		case strings.HasPrefix(p, "end_device-"):
			endDevice = p
			endDevicePath = prefix
			d.GenericDevice = "/dev/bsg/" + endDevice
		}
	}
	if endDevice == "" {
		return // not a SAS transport device
	}
	bay, err := os.ReadFile(endDevicePath + "/sas_device/" + endDevice + "/bay_identifier")
	if err == nil {
		d.EnclosureBay = strings.TrimSpace(string(bay))
	}
	switch {
	case d.Expander != "":
		d.ExpanderID = expanderAddress(d.ExpanderPath, d.Expander)
	case host != "":
		d.Expander, d.ExpanderPath = host, hostPath
		d.ExpanderID = sysAttr(hostPath+"/scsi_host/"+host, "host_sas_address")
	}
}

// expanderAddress reads the expander's SAS address from sysfs. It names
// the expander across boots, which the kernel's expander-H:N does not:
// H is the SCSI host number, which follows probe order. Empty when sysfs
// has none.
func expanderAddress(expanderPath, expander string) string {
	b, err := os.ReadFile(expanderPath + "/sas_device/" + expander + "/sas_address")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// newEmptyBayDevice describes an enclosure bay with nothing in it.
func newEmptyBayDevice(expander, expanderPath, bay string) *drivelist.Device {
	id := expanderAddress(expanderPath, expander)
	if strings.HasPrefix(expander, "host") {
		id = sysAttr(expanderPath+"/scsi_host/"+expander, "host_sas_address")
	}
	return &drivelist.Device{
		Expander:     expander,
		ExpanderID:   id,
		ExpanderPath: expanderPath,
		EnclosureBay: bay,
		Uses:         []string{"empty"},
	}
}
