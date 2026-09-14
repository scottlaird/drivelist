package collect

import (
	"regexp"
	"strings"

	"github.com/scottlaird/drivelist"
)

// annotateATAPorts gives a SATA drive on one of the board's own ports a
// place, so a hardware profile can say which bay the port feeds. The
// port is named as udev's ID_PATH does, "pci-0000:00:1f.2-ata-5": the
// controller's PCI address and the port number on it, which is fixed per
// board, unlike the kernel's ataN, which follows probe order. The
// enclosure is the chassis, as for NVMe. A SATA drive behind a SAS
// expander already has an SES place and is left alone; a drive on a USB
// bridge has no port.
func (c *Collector) annotateATAPorts(inv *drivelist.Inventory) error {
	key, model, board := dmiChassis(c.sys())
	if key == "" {
		return nil
	}
	for _, d := range inv.Devices {
		if d.EnclosureVia != "" || d.IsEmptyBay() {
			continue
		}
		port := ataPort(d)
		if port == "" {
			continue
		}
		d.EnclosureBay, d.EnclosureVia, d.EnclosureID, d.EnclosureViaID, d.EnclosureModel, d.EnclosureBoard = port, "ata", key, key, model, board
	}
	return nil
}

var ataIDPath = regexp.MustCompile(`^(pci-[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-9a-f]-ata-\d+)(\.\d+)?$`)

// ataPort is the drive's port on an onboard SATA controller, from ID_PATH
// with the device index dropped, or "" when the drive is not on one.
func ataPort(d *drivelist.Device) string {
	if m := ataIDPath.FindStringSubmatch(d.Attribs["ID_PATH"]); m != nil {
		return m[1]
	}
	// No ID_PATH (an old udev, or a fixture without one): the kernel's
	// name is the best there is.
	for _, p := range strings.Split(d.SysPath, "/") {
		if strings.HasPrefix(p, "ata") && len(p) > 3 && p[3] >= '0' && p[3] <= '9' {
			return p
		}
	}
	return ""
}
