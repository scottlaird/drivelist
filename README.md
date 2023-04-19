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


## Status

drivelist currently only supports SCSI-like drives on Linux.  SAS and
SATA are fine.  Actual SCSI drives might work, if you could find one
that actually works.  USB is untested, but if you have a dozen or more
USB drives then you have a *different* problem.

Currently, drivelist can identify drives in use by checking
mountpoints and by looking into ZFS pools and vdevs.  It successfully
deals with ZFS spares, log devices, pending resilvers, and so on.

Linux MD and LVM support is currently missing.  Currently, drives used
by either will show up as unused.  Stub code exists for detecting
each, but I don't have systems that use either right now.

## Roadmap

* Flesh out MD support
* Flesh out LVM support
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
* Add LCD support (via lcdctl or similar?) to flag specific drives for
  removal, etc.