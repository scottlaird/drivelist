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

This will leave a runable `drivelist` binary in 

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
