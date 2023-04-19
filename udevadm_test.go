package drivelist

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseUdevAdmData(t *testing.T) {
	data := `
P: /devices/pci0000:00/0000:00:03.0/0000:03:00.0/host11/port-11:0/expander-11:0/port-11:0:5/end_device-11:0:5/target11:0:12/11:0:12:0/block/sdaa
N: sdaa
L: 0
S: disk/by-id/scsi-SHITACHI_HUH72808CLAR8000_VJG3WK3X
S: disk/by-vdev/D22
S: disk/by-id/scsi-35000cca261071228
S: disk/by-id/wwn-0x5000cca261071228
S: disk/by-path/pci-0000:03:00.0-sas-exp0x5000ccab0200947e-phy11-lun-0
E: DEVPATH=/devices/pci0000:00/0000:00:03.0/0000:03:00.0/host11/port-11:0/expander-11:0/port-11:0:5/end_device-11:0:5/target11:0:12/11:0:12:0/block/sdaa
E: DEVNAME=/dev/sdaa
E: DEVTYPE=disk
E: DISKSEQ=35
E: MAJOR=65
E: MINOR=160
E: SUBSYSTEM=block
E: USEC_INITIALIZED=30193092
E: SCSI_TPGS=0
E: SCSI_TYPE=disk
E: SCSI_VENDOR=HITACHI
E: SCSI_VENDOR_ENC=HITACHI\x20
E: SCSI_MODEL=HUH72808CLAR8000
E: SCSI_MODEL_ENC=HUH72808CLAR8000
E: SCSI_REVISION=M7K0
E: ID_SCSI=1
E: ID_SCSI_INQUIRY=1
E: SCSI_IDENT_SERIAL=VJG3WK3X
E: SCSI_IDENT_LUN_NAA_REG=5000cca261071228
E: SCSI_IDENT_PORT_NAA_REG=5000cca261071229
E: SCSI_IDENT_PORT_RELATIVE=1
E: SCSI_IDENT_TARGET_NAA_REG=5000cca26107122b
E: SCSI_IDENT_TARGET_NAME=naa.5000CCA26107122B
E: ID_VENDOR=HITACHI
E: ID_VENDOR_ENC=HITACHI\x20
E: ID_MODEL=HUH72808CLAR8000
E: ID_MODEL_ENC=HUH72808CLAR8000
E: ID_REVISION=M7K0
E: ID_TYPE=disk
E: ID_WWN_WITH_EXTENSION=0x5000cca261071228
E: ID_WWN=0x5000cca261071228
E: ID_BUS=scsi
E: ID_SERIAL=35000cca261071228
E: ID_SERIAL_SHORT=5000cca261071228
E: ID_SCSI_SERIAL=VJG3WK3X
E: MPATH_SBIN_PATH=/sbin
E: DM_MULTIPATH_DEVICE_PATH=0
E: ID_PATH=pci-0000:03:00.0-sas-exp0x5000ccab0200947e-phy11-lun-0
E: ID_PATH_TAG=pci-0000_03_00_0-sas-exp0x5000ccab0200947e-phy11-lun-0
E: ID_PART_TABLE_UUID=89041a0d-2a91-0d49-9e81-0b4c9305eaa0
E: ID_PART_TABLE_TYPE=gpt
E: ID_VDEV=D22
E: ID_VDEV_PATH=disk/by-vdev/D22
E: DEVLINKS=/dev/disk/by-id/scsi-SHITACHI_HUH72808CLAR8000_VJG3WK3X /dev/disk/by-vdev/D22 /dev/disk/by-id/scsi-35000cca261071228 /dev/disk/by-id/wwn-0x5000cca261071228 /dev/disk/by-path/pci-0000:03:00.0-sas-exp0x5000ccab0200947e-phy11-lun-0
E: TAGS=:systemd:
E: CURRENT_TAGS=:systemd:
`
	got, err := parseUdevAdmData("/dev/sdaa", []byte(data))
	if err != nil {
		t.Fatalf("parseUdefAdmData returned error: %v", err)
	}

	want := &UdevAdmData{
		DeviceName: "sdaa",
		Attribs: map[string]string{
			"CURRENT_TAGS":              ":systemd:",
			"DEVLINKS":                  "/dev/disk/by-id/scsi-SHITACHI_HUH72808CLAR8000_VJG3WK3X /dev/disk/by-vdev/D22 /dev/disk/by-id/scsi-35000cca261071228 /dev/disk/by-id/wwn-0x5000cca261071228 /dev/disk/by-path/pci-0000:03:00.0-sas-exp0x5000ccab0200947e-phy11-lun-0",
			"DEVNAME":                   "/dev/sdaa",
			"DEVPATH":                   "/devices/pci0000:00/0000:00:03.0/0000:03:00.0/host11/port-11:0/expander-11:0/port-11:0:5/end_device-11:0:5/target11:0:12/11:0:12:0/block/sdaa",
			"DEVTYPE":                   "disk",
			"DISKSEQ":                   "35",
			"DM_MULTIPATH_DEVICE_PATH":  "0",
			"ID_BUS":                    "scsi",
			"ID_MODEL":                  "HUH72808CLAR8000",
			"ID_MODEL_ENC":              "HUH72808CLAR8000",
			"ID_PART_TABLE_TYPE":        "gpt",
			"ID_PART_TABLE_UUID":        "89041a0d-2a91-0d49-9e81-0b4c9305eaa0",
			"ID_PATH":                   "pci-0000:03:00.0-sas-exp0x5000ccab0200947e-phy11-lun-0",
			"ID_PATH_TAG":               "pci-0000_03_00_0-sas-exp0x5000ccab0200947e-phy11-lun-0",
			"ID_REVISION":               "M7K0",
			"ID_SCSI":                   "1",
			"ID_SCSI_INQUIRY":           "1",
			"ID_SCSI_SERIAL":            "VJG3WK3X",
			"ID_SERIAL":                 "35000cca261071228",
			"ID_SERIAL_SHORT":           "5000cca261071228",
			"ID_TYPE":                   "disk",
			"ID_VDEV":                   "D22",
			"ID_VDEV_PATH":              "disk/by-vdev/D22",
			"ID_VENDOR":                 "HITACHI",
			"ID_VENDOR_ENC":             `HITACHI\x20`,
			"ID_WWN":                    "0x5000cca261071228",
			"ID_WWN_WITH_EXTENSION":     "0x5000cca261071228",
			"MAJOR":                     "65",
			"MINOR":                     "160",
			"MPATH_SBIN_PATH":           "/sbin",
			"SCSI_IDENT_LUN_NAA_REG":    "5000cca261071228",
			"SCSI_IDENT_PORT_NAA_REG":   "5000cca261071229",
			"SCSI_IDENT_PORT_RELATIVE":  "1",
			"SCSI_IDENT_SERIAL":         "VJG3WK3X",
			"SCSI_IDENT_TARGET_NAA_REG": "5000cca26107122b",
			"SCSI_IDENT_TARGET_NAME":    "naa.5000CCA26107122B",
			"SCSI_MODEL":                "HUH72808CLAR8000",
			"SCSI_MODEL_ENC":            "HUH72808CLAR8000",
			"SCSI_REVISION":             "M7K0",
			"SCSI_TPGS":                 "0",
			"SCSI_TYPE":                 "disk",
			"SCSI_VENDOR":               "HITACHI",
			"SCSI_VENDOR_ENC":           `HITACHI\x20`,
			"SUBSYSTEM":                 "block",
			"TAGS":                      ":systemd:",
			"USEC_INITIALIZED":          "30193092",
		},
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("unexpected UdevAdmData (-want +got):\n%s", diff)
	}

}
