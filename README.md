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

You'll need a recent Go compiler installed (see `go.mod` for the
minimum version).  There are no C dependencies; the binary
cross-compiles.

```
  $ git clone https://github.com/scottlaird/drivelist.git
  $ cd drivelist
  $ go build ./cmd/drivelist
```

This will leave a runable `drivelist` binary in the current directory.

ZFS pool membership is read by running `zpool status`, so `zpool`
needs to be on the `PATH` of whoever runs drivelist; a host without it
simply reports no pools.  The original implementation linked `libzfs`
through cgo and is still in the tree behind `-tags libzfs` as a
reference, but it does not compile against OpenZFS 2.2 or later
headers.

On macOS and other non-Linux systems the tool builds and its tests
run, but it cannot enumerate drives, since it depends on Linux sysfs
and udev.

## Status

drivelist currently only supports SCSI-like drives (`sd*`) and NVMe
namespaces (`nvme*n*`) on Linux.  SAS and SATA are fine.  Actual SCSI
drives might work, if you could find one that actually still works.
USB probably works, but if you have a dozen or more USB drives on one
system then you have a *different* problem.

A drive that the kernel lists but `udevadm` cannot describe is still
shown, with its kernel name and the failure in the `error` field, and
a note on stderr.  One unresponsive drive does not hide the others.

Currently, drivelist can identify drives in use by checking
mountpoints and by looking into ZFS pools and vdevs.  It successfully
deals with ZFS spares, log, cache and special devices, pending
resilvers, and so on.  A pool member that no present device matches
(a drive that has failed completely, been pulled, or is installed but
invisible to the block layer) is reported on stderr.

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
...
```
The `drivelist` tool collects more data than can reasonably be shown on one line in a terminal; adding the `--allfields` flag will show all fields.

```
$ ./drivelist --allfields
Device Name     Devices                                                                                                                                                                                                                                                                                                                                                                                                                                  WWN                     Sys Path                                                                                                                                                 Model                   Serial                  Uses                                                                                                                                                                                                     Generic Device                  Expander        Expander Path                                                                            Bay     Size
===========     =======                                                                                                                                                                                                                                                                                                                                                                                                                                  ===                     ========                                                                                                                                                 =====                   ======                  ====                                                                                                                                                                                                     ==============                  ========        =============                                                                            ===     ====
sda             /dev/sda,/dev/disk/by-id/scsi-SHGST_HUH721008AL5204_7SG3RM2G,/dev/disk/by-path/pci-0000:01:00.0-sas-exp0x500304800000007f-phy8-lun-0,/dev/disk/by-vdev/Ab0,/dev/disk/by-id/wwn-0x5000cca25206c808,/dev/disk/by-id/scsi-35000cca25206c808                                                                                                                                                                                                 0x5000cca25206c808      /sys/devices/pci0000:00/0000:00:01.0/0000:01:00.0/host4/port-4:0/expander-4:0/port-4:0:0/end_device-4:0:0/target4:0:0/4:0:0:0/block/sda                  HUH721008AL5204         7SG3RM2G                zfs > space 5925914041408872576 > raidz2 1731578064309998343 > disk 1837064446111292648                                                                                                                  /dev/bsg/end_device-4:0:0       expander-4:0    //sys/devices/pci0000:00/0000:00:01.0/0000:01:00.0/host4/port-4:0/expander-4:0           0       8 TB
sdaa            /dev/sdaa,/dev/disk/by-id/scsi-SHGST_H7280A520SUN8.0T_001619PJLREV_VKJJLREV,/dev/disk/by-path/pci-0000:03:00.0-sas-exp0x5000ccab0200947e-phy12-lun-0,/dev/disk/by-vdev/D54,/dev/disk/by-id/wwn-0x5000cca2548eecec,/dev/disk/by-id/scsi-35000cca2548eecec                                                                                                                                                                                 0x5000cca2548eecec      /sys/devices/pci0000:00/0000:00:03.0/0000:03:00.0/host11/port-11:0/expander-11:0/port-11:0:6/end_device-11:0:6/target11:0:13/11:0:13:0/block/sdaa        H7280A520SUN8.0T        001619PJLREV_VKJJLREV   zfs > space 5925914041408872576 > raidz2 5236016460003016805 > disk 9810514795403010748                                                                                                                  /dev/bsg/end_device-11:0:6      expander-11:0   //sys/devices/pci0000:00/0000:00:03.0/0000:03:00.0/host11/port-11:0/expander-11:0       54       8 TB
sdab            /dev/sdab,/dev/disk/by-id/scsi-35000cca26103eb9c,/dev/disk/by-vdev/D10,/dev/disk/by-id/scsi-SHITACHI_HUH72808CLAR8000_VJG24UZX,/dev/disk/by-path/pci-0000:03:00.0-sas-exp0x5000ccab0200947e-phy14-lun-0,/dev/disk/by-id/wwn-0x5000cca26103eb9c                                                                                                                                                                                           0x5000cca26103eb9c      /sys/devices/pci0000:00/0000:00:03.0/0000:03:00.0/host11/port-11:0/expander-11:0/port-11:0:7/end_device-11:0:7/target11:0:14/11:0:14:0/block/sdab        HUH72808CLAR8000        VJG24UZX                zfs > space 5925914041408872576 > raidz2 12372305547527317295 > disk 4484622911110778645                                                                                                                 /dev/bsg/end_device-11:0:7      expander-11:0   //sys/devices/pci0000:00/0000:00:03.0/0000:03:00.0/host11/port-11:0/expander-11:0       10       8 TB
...
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
filesystems as well as drives that are part of ZFS pools, leaving only
unused devices.  This is very useful with large numbers of drives, as
it's easy to lose track of physically present drives during
migrations, etc, and end up with installed and powered-on but unused
disks in a system with many drives.

It's also useful for identifying failed devices that have been removed
from ZFS but not yet physically removed from the system.

```
$ ./drivelist --unused
Device Name     Model           WWN                     Serial          Expander        Bay     Size
===========     =====           ===                     ======          ========        ===     ====
sdal            PX02SMU020      0x500003964c8806e4      5520A0CAT2AA    expander-11:0   30
sdbt            HUH728080ALE601 0x5000cca260c165e2      VLG32AEY        expander-11:1   17      8 TB
```

## Fleet tracking

drivelist can also run as a small fleet service: every host with
drives reports its inventory to one server, the server keeps a
lifetime history per drive, and the CLI answers questions like "where
is serial VJG24UZX, where has it been, and when did it go missing."

Run the server somewhere with two bearer tokens, one for agents and
one for operators:

```
  $ echo agent-secret > /etc/drivelist/agent-token
  $ echo operator-secret > /etc/drivelist/operator-token
  $ drivelist serve --db /var/lib/drivelist/drivelist.db --listen :9450 \
      --agent-token-file /etc/drivelist/agent-token \
      --operator-token-file /etc/drivelist/operator-token
```

On each host, run the agent, which reports every five minutes (the
server can change the interval), spools reports while the server is
unreachable and replays them in order afterwards, and writes the
server's status for each local drive to `/var/lib/drivelist/status.json`
so the plain `drivelist` listing gains a `status` column:

```
  $ DRIVELIST_SERVER=fleet:9450 DRIVELIST_AGENT_TOKEN=agent-secret drivelist agent
```

The agent also follows `/dev/kmsg`.  When the kernel attaches or
removes a disk it reports again a few seconds later (after udev has
settled), so a drive's appearance or disappearance is timestamped
within seconds rather than at the next tick.  Drive errors in the log
are classified (a SCSI additional sense code of 0x5D is a predictive
failure whatever the sense key says; medium, hardware and I/O errors,
timeouts, link resets, and plain recovered errors are told apart) and
counted per hour.  `--kmsg=false` turns the follower off.

SMART is sampled through `smartctl -j` (smartmontools 7 or later): a
baseline pass for every drive when the agent starts, a full pass every
six hours (the server can change it), and a sample of any drive that
newly appears or that the kernel reports a predictive failure, medium
error or hardware error for.  Sleeping drives are left asleep and
recorded as skipped.  The summary (health, hours, temperature,
reallocated, pending and uncorrectable sectors, CRC errors, bytes read
and written, wear, last self-test) is sent every time; the full
smartctl JSON at most daily per drive, or when the summary changed.
`drivelist drive REF smart` shows the samples and `--raw` the newest
JSON.  `--smart=false` turns sampling off.

I/O statistics come from `/proc/diskstats`, read every minute and
folded into one bucket per drive per hour (counts, bytes, time, and
the worst minute's latency and utilisation), sent when the hour ends.
`drivelist drive REF io` shows a drive's buckets and `drivelist io
compare [--host H] [--since 24h]` lists every drive's read and write
latency and utilisation next to the median of its vdev, worst first,
which is how a drive that is three times slower than its otherwise
identical siblings shows up.  Hourly buckets are kept for 180 days and
then rolled into daily ones.  `--io=false` turns sampling off.

For a one-off report, or from cron, `drivelist report` does one cycle.

On Debian and Ubuntu hosts, install the package instead.  Every
[release](https://github.com/scottlaird/drivelist/releases) carries
`drivelist_<version>_<arch>.deb` for `amd64`, `arm64` (64-bit
Raspberry Pi OS) and `armhf` (32-bit Raspberry Pi OS), built by the
release workflow when a `v*` tag is pushed; `make deb` builds the same
packages locally (it needs [nfpm](https://nfpm.goreleaser.com/)), and
`make deb-arm64` just one.  The package ships the binary, a
`drivelist-agent` systemd service, a disabled `drivelist-server`
service, and `/etc/default/drivelist`.  After installing, put the
server address in `/etc/default/drivelist`, the agent token in
`/etc/drivelist/agent-token`, and `systemctl start drivelist-agent`;
the service is enabled but stays inert until the token file exists.

Then, from anywhere, with `DRIVELIST_SERVER` and
`DRIVELIST_OPERATOR_TOKEN` set or written as `server = …` and
`operator_token = …` in `~/.config/drivelist/config`:

```
  $ drivelist hosts
  $ drivelist drives [--host fs2] [--status bad,suspect] [--unused] [--missing]
  $ drivelist drive VJG24UZX
  $ drivelist drive VJG24UZX history
  $ drivelist drive VJG24UZX mark suspect --note "r_await 3x siblings"
  $ drivelist drive VJG24UZX note "RMA 4471 opened"
  $ drivelist events [--since 24h] [--kind vanished,moved_host] [--host fs2]
  $ drivelist missing
```

The server exposes Prometheus metrics at `/metrics` without a token:
drives, missing drives, ghosts, staleness and last report time per
host; known drives by status; kernel warnings and events in the last
24 hours; and report counts and ingest latency.  Per-drive values are
not exported; they live in the database and the CLI.

A drive is referred to by serial, WWN, or an unambiguous prefix of
either.  Every command takes `--json` for the raw response.  A drive
that a complete report no longer lists is recorded as vanished with
the last time it was confirmed; a host that stops reporting is marked
stale and its drives are left in place, since only a report from the
host itself can say a drive is gone.  Marking a drive `bad`,
`shelved`, or `retired` means its absence is expected and it drops
out of `missing`.

## Testing and fixtures

The collector reads sysfs, `/proc/self/mountinfo`, and the output of
`udevadm`. All three are injectable, and `collect/testdata/` holds
captured trees that the tests run against on any OS, including macOS.
To capture a fixture from a real system:

```
  $ drivelist capture /tmp/myhost
```

This writes the parts of sysfs the collector reads, the mount table,
the `udevadm` output for every disk, and the output of several
`zpool` commands (skipped where they fail) in the layout
`collect.Fixture` expects.  Note that it records the serial numbers
and WWNs of every drive in the system.

Captured trees under `collect/testdata/` each carry an
`inventory.json` golden file of what the collector produces from
them.  After an intended change to the collector, regenerate them
with `go test ./collect -update` and review the diff.

## Roadmap

* Improve testing: capture fixtures from more real systems (see
  `--capture`) and find a reasonable way to fake zfs.PoolOpenAll().
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
