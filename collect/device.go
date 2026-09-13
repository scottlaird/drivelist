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

// populateSES walks the device's sysfs path for the SAS expander and end
// device it hangs off, and reads where the end device says it is: the SES
// enclosure and the bay in it. A drive on the HBA's own ports has no
// expander but still an enclosure (the backplane the HBA's SGPIO talks
// to), so its location is as real as a shelf's.
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
		return // not on the SAS transport
	}
	attrs := endDevicePath + "/sas_device/" + endDevice
	d.EnclosureBay = sysAttr(attrs, "bay_identifier")
	d.EnclosureID = enclosureID(sysAttr(attrs, "enclosure_identifier"))
	switch {
	case d.Expander != "":
		d.ExpanderID = expanderAddress(d.ExpanderPath, d.Expander)
		d.EnclosureVia, d.EnclosureViaID = d.Expander, d.ExpanderID
	case host != "":
		d.EnclosureVia, d.EnclosureViaID = host, sysAttr(hostPath+"/scsi_host/"+host, "host_sas_address")
	}
}

// enclosureID normalises an enclosure_identifier: some hardware reports
// none as 0.
func enclosureID(s string) string {
	if strings.Trim(strings.ToLower(s), "0x") == "" {
		return ""
	}
	return s
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

// newEmptyBayDevice describes an enclosure bay with nothing in it, from
// the end device that represents the bay. via is the node that reaches
// it: an expander, or the HBA for its own bays.
func newEmptyBayDevice(via, viaPath, bayDevice, bay string) *drivelist.Device {
	d := &drivelist.Device{
		EnclosureBay:   bay,
		EnclosureID:    enclosureID(sysAttr(bayDevice, "enclosure_identifier")),
		EnclosureVia:   via,
		EnclosureViaID: sysAttr(viaPath+"/scsi_host/"+via, "host_sas_address"),
		Uses:           []string{"empty"},
	}
	if strings.HasPrefix(via, "expander-") {
		d.Expander, d.ExpanderPath = via, viaPath
		d.ExpanderID = expanderAddress(viaPath, via)
		d.EnclosureViaID = d.ExpanderID
	}
	return d
}
