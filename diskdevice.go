package drivelist

import (
	"bytes"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/golang/glog"
)

type DiskDevice struct {
	DeviceName    string
	Devices       []string // Names in /dev that refer to this disk
	WWN           string
	SysPath       string
	Model         string
	Serial        string
	Attribs       map[string]string
	Uses          []string
	GenericDevice string
	// SCSI path
	// Want host -> enclosure... -> end_device, with PHY info along the way.

	// Enclosure data
	Expander     string
	ExpanderPath string
	EnclosureBay string

	// Size
	Size uint64 // bytes
}

func NewDiskDevice(name string) (*DiskDevice, error) {
	d := &DiskDevice{}

	u, err := GetUdevAdmData(name)
	if err != nil {
		return d, err
	}
	d.Attribs = u.Attribs
	d.DeviceName = u.DeviceName
	d.SysPath = "/sys" + d.Attribs["DEVPATH"]
	d.WWN = d.Attribs["ID_WWN"]
	d.Model = d.Attribs["ID_MODEL"]
	d.Serial = d.Attribs["SCSI_IDENT_SERIAL"]
	d.Uses = []string{}

	d.Devices = []string{"/dev/" + d.DeviceName} // Add /dev/sdX
	for _, i := range strings.Split(d.Attribs["DEVLINKS"], " ") {
		d.Devices = append(d.Devices, i)
	}

	err = d.PopulateSESData()
	if err != nil {
		return d, err
	}

	sizeString, err := os.ReadFile("/sys/class/block/" + d.DeviceName + "/size")
	if err != nil {
		glog.Errorf("Error reading file: %v\n", err)
	} else {
		sizeBlocks, _ := strconv.ParseUint(strings.TrimSuffix(string(sizeString), "\n"), 10, 64)
		d.Size = sizeBlocks * 512
	}

	return d, nil
}

func NewEmptyBayDiskDevice(expander string, expanderPath string, bay string) (*DiskDevice, error) {
	d := &DiskDevice{}

	d.Expander = expander
	d.ExpanderPath = expanderPath
	d.EnclosureBay = bay

	d.Uses = []string{"empty"} // Maybe?

	return d, nil
}

func (d *DiskDevice) PopulateSESData() error {
	var prefix, endDevice, endDevicePath string

	syspath := strings.Split(d.SysPath, "/")
	for _, p := range syspath {
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
		// Bay info should be in endDevicePath/sas_device/endDevice/bay_identifier
		bay, err := os.ReadFile(endDevicePath + "/sas_device/" + endDevice + "/bay_identifier")
		if err == nil {
			d.EnclosureBay = strings.TrimSuffix(string(bay), "\n")
		}
	}
	return nil
}

func (d *DiskDevice) WriteTabs(w io.Writer) {
	buf := bytes.Buffer{}
	for _, f := range FieldFlag {
		switch f {
		case "devicename":
			buf.WriteString(d.DeviceName)
			buf.WriteString("\t")
		case "devices":
			buf.WriteString(strings.Join(d.Devices, ","))
			buf.WriteString("\t")
		case "wwn":
			buf.WriteString(d.WWN)
			buf.WriteString("\t")
		case "syspath":
			buf.WriteString(d.SysPath)
			buf.WriteString("\t")
		case "model":
			buf.WriteString(d.Model)
			buf.WriteString("\t")
		case "serial":
			buf.WriteString(d.Serial)
			buf.WriteString("\t")
		case "uses":
			buf.WriteString(strings.Join(d.Uses, ","))
			buf.WriteString("\t")
		case "genericdevice":
			buf.WriteString(d.GenericDevice)
			buf.WriteString("\t")
		case "expander":
			buf.WriteString(d.Expander)
			buf.WriteString("\t")
		case "expanderpath":
			buf.WriteString(d.ExpanderPath)
			buf.WriteString("\t")
		case "bay":
			buf.WriteString(d.EnclosureBay)
			buf.WriteString("\t")
		case "size":
			buf.WriteString(FormatDiskSize(d.Size))
			buf.WriteString("\t")
		default:
			panic("unknown field")
		}
	}

	buf.WriteString("\n")
	buf.WriteTo(w)
}
