drivelist is a tool to make managing large numbers of drives on Linux
systems easier.  On my system, it's able to walk through roughly 100
drives in about a half second and identify how they're used.

## Features:

* It can enumerate all drives and show device type, serial number,
  WWN, enclosure, and bay for each, making it easier to physically
  locate drives.
* It can identify which drives are in use and easily show unused
  drives (via the --unused flag)
* It can show empty/unused enclosure bays, which may help spot dead
  drives.

## Building

You'll need to have a recent Go compiler installed.  Then do something like this:

```
  $ go checkout https://github.com/scottlaird/drivelist.git
  $ go get
  $ go build cmd/drivelist/drivelist.go
```

This will leave a runable `drivelist` binary in the current directory.

## Status

drivelist currently only supports SCSI-like drives on Linux.  SAS,
SATA, and NVMe are fine.  Actual SCSI drives might work, if you could
find one that actually still works.  USB probably works, but if you
have a dozen or more USB drives on one system then you have a
*different* problem.

Currently, drivelist can identify drives in use by checking
mountpoints and by looking into ZFS pools and vdevs.  It successfully
deals with ZFS spares, log devices, pending resilvers, and so on.

Linux MD, LVM, and btrfs support is currently missing.  Stub code
exists for MD and LVM, but I'm not currently using either.

## Usage

By default, running `drivelist` will show all devices in the current system along with a bit of data about each.

Example:

```
$ ./drivelist
Device Name     Model                   WWN                     Serial                  Expander        Bay     Size
===========     =====                   ===                     ======                  ========        ===     ====
sda             HUH721008AL5204         0x5000cca25206c808      7SG3RM2G                expander-4:0    0       8 TB
sdaa            H7280A520SUN8.0T        0x5000cca2548eecec      001619PJLREV_VKJJLREV   expander-11:0   54      8 TB
sdab            HUH72808CLAR8000        0x5000cca26103eb9c      VJG24UZX                expander-11:0   10      8 TB
sdac            PX02SMQ160              0x500003964c8806c4      5520A0C2T2AA            expander-11:0   33      1600 GB
sdad            HUH72808CLAR8000        0x5000cca261035808      VJG1V09X                expander-11:0   20      8 TB
sdae            PX02SMQ160              0x500003964c8800ec      5520A01OT2AA            expander-11:0   32      1600 GB
sdaf            PX02SMQ160              0x500003964c8806bc      5520A0C0T2AA            expander-11:0   31      1600 GB
sdag            H7280A520SUN8.0T        0x5000cca2548d06a4      001619PHKAHV_VKJHKAHV   expander-11:0   53      8 TB
sdah            TOSHIBA_HDWG180         0x5000039b08ca908c      7160A2TZFBEG            expander-11:0   42      8 TB
sdai            HUH72808CLAR8000        0x5000cca23bb2bb48      2EK595JX                expander-11:0   19      8 TB
sdaj            HGST_HUS728T8TAL        0x5000cca0bee6a714      VGJS0Y6K                expander-11:0   8       8 TB
sdak            HGST_HUS728T8TAL        0x5000cca0bbe6cf6c      VDJSBPBK                expander-11:0   7       8 TB
...
```
The `drivelist` tool collects more data than can reasonably be shown on one line in a terminal; adding the `--allfields` flag will show all fields.

```
$ ./drivelist --allfields
Device Name     Devices                                                                                                                                                                                                                                                                                                                                                                                                                                  WWN                     Sys Path                                                                                                                                                 Model                   Serial                  Uses                                                                                                                                                                                                     Generic Device                  Expander        Expander Path                                                                            Bay     Size
===========     =======                                                                                                                                                                                                                                                                                                                                                                                                                                  ===                     ========                                                                                                                                                 =====                   ======                  ====                                                                                                                                                                                                     ==============                  ========        =============                                                                            ===     ====
sda             /dev/sda,/dev/disk/by-id/scsi-SHGST_HUH721008AL5204_7SG3RM2G,/dev/disk/by-path/pci-0000:01:00.0-sas-exp0x500304800000007f-phy8-lun-0,/dev/disk/by-vdev/Ab0,/dev/disk/by-id/wwn-0x5000cca25206c808,/dev/disk/by-id/scsi-35000cca25206c808                                                                                                                                                                                                 0x5000cca25206c808      /sys/devices/pci0000:00/0000:00:01.0/0000:01:00.0/host4/port-4:0/expander-4:0/port-4:0:0/end_device-4:0:0/target4:0:0/4:0:0:0/block/sda                  HUH721008AL5204         7SG3RM2G                zfs > space 5925914041408872576 > raidz2 1731578064309998343 > disk 1837064446111292648                                                                                                                  /dev/bsg/end_device-4:0:0       expander-4:0    //sys/devices/pci0000:00/0000:00:01.0/0000:01:00.0/host4/port-4:0/expander-4:0           0       8 TB
sdaa            /dev/sdaa,/dev/disk/by-id/scsi-SHGST_H7280A520SUN8.0T_001619PJLREV_VKJJLREV,/dev/disk/by-path/pci-0000:03:00.0-sas-exp0x5000ccab0200947e-phy12-lun-0,/dev/disk/by-vdev/D54,/dev/disk/by-id/wwn-0x5000cca2548eecec,/dev/disk/by-id/scsi-35000cca2548eecec                                                                                                                                                                         0x5000cca2548eecec      /sys/devices/pci0000:00/0000:00:03.0/0000:03:00.0/host11/port-11:0/expander-11:0/port-11:0:6/end_device-11:0:6/target11:0:13/11:0:13:0/block/sdaa        H7280A520SUN8.0T        001619PJLREV_VKJJLREV   zfs > space 5925914041408872576 > raidz2 5236016460003016805 > disk 9810514795403010748                                                                                                                  /dev/bsg/end_device-11:0:6      expander-11:0   //sys/devices/pci0000:00/0000:00:03.0/0000:03:00.0/host11/port-11:0/expander-11:0       54       8 TB
sdab            /dev/sdab,/dev/disk/by-id/scsi-35000cca26103eb9c,/dev/disk/by-vdev/D10,/dev/disk/by-id/scsi-SHITACHI_HUH72808CLAR8000_VJG24UZX,/dev/disk/by-path/pci-0000:03:00.0-sas-exp0x5000ccab0200947e-phy14-lun-0,/dev/disk/by-id/wwn-0x5000cca26103eb9c                                                                                                                                                                                           0x5000cca26103eb9c      /sys/devices/pci0000:00/0000:00:03.0/0000:03:00.0/host11/port-11:0/expander-11:0/port-11:0:7/end_device-11:0:7/target11:0:14/11:0:14:0/block/sdab        HUH72808CLAR8000        VJG24UZX                zfs > space 5925914041408872576 > raidz2 12372305547527317295 > disk 4484622911110778645                                                                                                         /dev/bsg/end_device-11:0:7      expander-11:0   //sys/devices/pci0000:00/0000:00:03.0/0000:03:00.0/host11/port-11:0/expander-11:0       10       8 TB
```

There is also a `--fields=` flag for picking specific fields:

```
$ ./drivelist --fields=devicename,genericdevice,size
Device Name     Generic Device                  Size
===========     ==============                  ====
sda             /dev/bsg/end_device-4:0:0       8 TB
sdaa            /dev/bsg/end_device-11:0:6      8 TB
sdab            /dev/bsg/end_device-11:0:7      8 TB
sdac            /dev/bsg/end_device-11:0:8      1600 GB
sdad            /dev/bsg/end_device-11:0:9      8 TB
...
```

Finally, there is an `--unused` flag that only shows devices with no
known use in the system.  This filters out drives that have mounted
filesystems as well as drives that are part of ZFS pools.

```
$ ./drivelist --unused
Device Name     Model           WWN                     Serial          Expander        Bay     Size
===========     =====           ===                     ======          ========        ===     ====
sdal            PX02SMU020      0x500003964c8806e4      5520A0CAT2AA    expander-11:0   30
sdbt            HUH728080ALE601 0x5000cca260c165e2      VLG32AEY        expander-11:1   17      8 TB
```

## Roadmap

* Improve testing.
  * Add a `testdata/` directory with multiple sets of test `/sys` and
    `/proc` data, and run tests across each.
  * Find a reasonable way to fake zfs.PoolOpenAll().
* Flesh out MD support
* Flesh out LVM support
* Add btrfs support
* Verify/fix USB support
* Improve filtering; make it possible to show all devices, empty drive
  bays, or any reasonable subset.
* Add CSV output, simple tab-delmited output, and make headers
  optional.
* Add sorting
* Consider adding state, ideally shared over the network across
  multiple servers.
  * Mark disks as bad and see them flagged wherever they appear.
  * Answer "when did this disk first appear?" and other lifecycle
    questions.
  * Track transient issues, past uses, etc, as make sense.
* Add better SAS support, to identify link paths, speeds, and widths.
* Add support for managing SAS enclosure LEDs (via lcdctl or similar?)
  to flag specific drives for removal, etc.
