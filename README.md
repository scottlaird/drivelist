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

On macOS, drives are enumerated through `diskutil` and identified
(model and serial) through `system_profiler`, with mounted volumes as
their uses; there are no WWNs, enclosure bays or ZFS states there, but
a Mac can run the agent and its drives join the fleet.  Other systems
build and run the server and CLI but cannot enumerate drives.

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
timeouts, link resets, and plain recovered errors are told apart; a
sense code of 0x0B is the drive's own warning, a vendor notice or a
background scan result, counted as a warning and answered with a SMART
sample rather than treated as an error) and counted per hour.  The
kernel prints the text rather than the hex for codes it knows, and
those lines are read the same way.  Errors about the host itself,
memory and machine-check errors as EDAC, the MCE decoder and APEI
report them, are counted too and become `hardware_error` events on
the host, once per class, location and day: a DIMM throwing corrected
errors is usually the warning before an uncorrected one takes the
machine down.  `--kmsg=false` turns the follower off.

Every report also carries the host's memory modules: what the firmware
says of each slot (SMBIOS type 17: the printed slot name, size, type,
speed, ranks, part and serial number) joined to what the kernel's EDAC
driver counts for it (corrected and uncorrected errors since boot).
The join uses the firmware's bank locator when it names the channel and
DIMM index (`P0_Node0_Channel1_Dimm1`), else the channel letter of the
slot name, which is exact when the channel holds one module and a guess
by slot order when it holds two; `drivelist dimms --allfields` shows
the EDAC location and how sure the match is.  `drivelist dimms
[--host H] [--problems]` lists the fleet's memory, modules with errors
first; the web interface has the same under Memory and on each host's
page.  Counts that grew since the last report leave a sample and a
`memory_errors` event, once per module per day; a module appearing,
vanishing or changing serial number in its slot is a `dimm_changed`
event; and a `hardware_error` event on a host that has reported its
memory names the slot.  A corrected error a second on one module, as
a failing DDR5 module produces, is the warning before the uncorrected
one that takes the machine down.

Every report carries the kernel's boot id and boot time.  A report
with a new boot id is a `host_rebooted` event, timestamped at the
boot, saying how long the previous boot had been reporting and how
long the host was silent, so a crash shows up even when the host was
back before it went stale.  `drivelist hosts` shows each host's
uptime.  A new boot also restarts the SAS error counters, which count
since boot, so boot-time link training is not read as counter growth.

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
JSON; `drivelist smart [--host H] [--problems]` lists every drive's
newest reading, problems first (health failed, an error counter
nonzero, 80% of rated life used).  `--smart=false` turns sampling off.

I/O statistics come from `/proc/diskstats`, read every minute and
folded into one bucket per drive per hour (counts, bytes, time, and
the worst minute's latency and utilisation), sent when the hour ends.
`drivelist drive REF io` shows a drive's buckets and `drivelist io
compare [--host H] [--since 24h]` lists every drive's read and write
latency and utilisation next to the median of its vdev, worst first,
which is how a drive that is three times slower than its otherwise
identical siblings shows up.  Hourly buckets are kept for 180 days and
then rolled into daily ones.  `--io=false` turns sampling off.

Some kernels (6.18.38 and 6.18.39, 7.1.3 and 7.1.4, and distribution
kernels that took the same patch, such as Ubuntu 26.04's 7.0.0-31)
occasionally account an I/O from a zero start time, which adds the
host's uptime to the time counters and shows up in `iostat` as a
multi-second await at 1% utilisation.  The agent reads `/proc/uptime`
alongside `/proc/diskstats` and drops the latency of any minute whose
read or write time jumped by about the uptime; the minute's
completions and bytes still count.  The DROPPED column of
`drivelist drive REF io` says how many counters an hour lost.

A drive's slot is the SES enclosure it sits in plus its bay there,
read from the drive's own end device in sysfs, so it is the same for a
drive in a shelf behind an expander and for one on the HBA's own ports
(a server's front panel), and it survives reboots, which renumber the
kernel's `expander-H:N`.  When a whole enclosure comes back under a
new key with every drive in its old bay, the server records one
`enclosure_renamed` event rather than a move per drive (a single drive
is not enough evidence; it may really have moved), and a name given to
the old key follows it.  `drivelist enclosures` lists every enclosure
a host has reported, drives or not, with the model of the expander or
HBA that reaches it, and `drivelist enclosure KEY name "front shelf"`
gives one a name, which every listing then shows in place of the
kernel's.

NVMe drives have no SES, but a U.2 bay is a PCIe hotplug slot, and the
firmware's slot table (`/sys/bus/pci/slots`) says which slot each
drive's controller is in.  Those drives get the chassis as their
enclosure, keyed on its DMI serial and described by vendor and product
name, with the firmware's slot name as the bay (`9-1` is the second
lane of a bifurcated slot 9).  The sibling lanes of an occupied slot
with nothing behind them are reported as empty bays; empty add-in
card slots cannot be told from empty bays and are left alone.  A
drive in no hotplug slot (an M.2, or a U.2 on a board without hotplug
tables) falls back to SMBIOS type 9, which names every slot the board
vendor cared to describe (`M.2_1`, `PCIE3`, or a bare reference
designator like `J3502`) by the root port it hangs off; most consumer
boards describe some slots and not others, so a drive in a slot the
vendor left out is placed by its root port's address instead
(`0000:00:01.3`), which is as fixed per board as a designation and
lets a profile name the slot anyway.  A carrier card with a PCIe
switch puts several drives behind one slot; the switch ports between
the slot and each drive are appended as `PCI-E Slot 6/00.0/08.0`, so
a profile can name each M.2 on the card.  A SATA drive on one of the board's own ports is
placed by the port, named as udev names it (`pci-0000:00:1f.2-ata-5`,
the controller and the port number on it), so a profile can say which
bay the port feeds.

Firmware identities are not what a person calls a bay.  `hardware/`
holds a profile per enclosure model, embedded in the binary: what the
manual calls each bay, which firmware identities land in it (a bay
wired for both U.2 and SATA lists a PCIe slot and an ATA port), how
the bays are arranged, and which bays exist even when nothing has
been seen in them.  The server applies profiles as a view: placements
keep the firmware bay, and every listing shows the profile's name for
it.  `drivelist enclosure KEY bays` shows an enclosure bay by bay in
the profile's layout, then any occupied bay the profile does not
know; `drivelist hardware` lists the profiles built in and
`drivelist hardware check HOST` says which of a host's enclosures
matched one and which occupied bays no profile names, which is what
a profile for a new box needs.  Try a profile with `--dir` (and
`serve --hardware-dir`) before adding it to `hardware/profiles/`.

Every listing (`hosts`, `drives`, `smart`, `enclosures`, `missing`,
`io compare`, `sas errors`, `hardware`) takes `--fields a,b,c` to
choose and order its columns, `--allfields` for every column the
listing has (the help text names them), and `--sort a,-b` to sort by
columns, a leading `-` for descending; sizes, counters and times sort
as what they are, not as text, and unknown values sort last.

The server also serves a read-only web interface at `/ui/` (`/`
redirects there): the same listings as the command line with sortable
columns and a column picker, and every host, drive, enclosure and bay
a link to its own page.  It asks for a token once and keeps it in the
browser; give it the optional viewer token (`serve --viewer-token-file`
or `DRIVELIST_VIEWER_TOKEN`, `/etc/drivelist/viewer-token` for the
package), which can read everything and change nothing, so the
operator token never has to leave your shell.  The page builds every
element from text, never from markup, and is served under a
Content-Security-Policy that admits no inline script, so nothing that
reaches the database can run in the browser.  A host's SAS page draws
the topology, host to HBAs to expanders to the bays of each enclosure,
wide ports as heavy lines labelled with rate and width, every bay a
link to its drive with the detail in a tooltip; the drawing needs
[Mermaid](https://mermaid.js.org/), which the page loads from the one
CDN path the policy names.

`drivelist demo export DIR --names demo/names.json` writes a copy of
the web interface that needs no server: the page, marked as a demo,
and one file per question it can ask, with every answer sanitized as
the names file says (hosts renamed, other hosts numbered, literal
replacements, fields emptied; the numbering is kept in
`DIR/mapping.json` between exports).  The page's clock stops at the
export, so relative times stay true.  `demo/publish.sh DIR` pushes it
as the `gh-pages` branch, one fresh commit each time; anything that
serves files will do as well.  Review `DIR/data` before publishing:
the export keeps kernel messages, notes and serial numbers as they
are.

For a one-off report, or from cron, `drivelist report` does one cycle.
A host you would rather not give a token to can still be tracked: run
`drivelist report --output - [--smart]` on it (no server, no token)
and pipe the result to `drivelist admin ingest -` on a host that
holds the operator token, which the server accepts for reports too:

    ssh web1 sudo drivelist report --output - --smart | drivelist admin ingest -q -

The report lands as web1, with a SMART pass when asked for.  The
bundle also carries the host's `/proc/diskstats` counters, which the
server diffs against the previous pull's, so pulling on a schedule
gives the host I/O buckets like an agent's, one per interval (the
first pull, and the first after a reboot, only set the baseline; a
gap over two days is not bucketed).  What such a host still lacks is
the agent's kernel log watch.  `-q` keeps cron quiet; from cron, one
line pulls several hosts:

    17 * * * * scott for h in web1 web2; do ssh -o BatchMode=yes $h sudo drivelist report --output - --smart | drivelist admin ingest -q -; done

`drivelist sas` prints the host's SAS topology from sysfs: each HBA and
expander with its phys, the port each phy is bundled into (a wide port
is several phys sharing one), the negotiated link rate, what is on the
far end (an expander, or a drive with its bay), and the four SAS error
counters the kernel keeps per phy since boot: invalid dwords, running
disparity errors, loss of dword sync, and phy reset problems.
`--errors` shows only phys with a nonzero counter, which is the quick
way to find the cable or backplane lane that is going bad.

The agent sends the topology with every inventory report, and the
server keeps each host's last view of it (`drivelist sas HOST`) and
turns changes into events: a phy's link rate changing or its link
coming and going (`sas_link_changed`), something else on the far end
of a phy (`sas_attached_changed`), a wide port bundling a different
number of phys (`sas_port_changed`), and an HBA or expander appearing,
vanishing, or changing firmware (`sas_node_changed`).  Events on a
phy that leads to a drive are attributed to that drive, so they show
in its history beside SMART and kernel warnings.  When a phy's error
counters grow between reports the growth is kept as a sample and a
`sas_errors` event is recorded, at most once per phy per day;
`drivelist sas errors [--host H] [--since 7d]` sums the growth per
phy over a window, and `/metrics` exports every present phy's
counters and link rate for graphing.

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

On macOS, `make` builds the binary for the machine it runs on, and
`packaging/net.scottstuff.drivelist-agent.plist` runs the agent as a
launchd daemon.  The agent reports the inventory and, with Homebrew's
smartmontools installed, SMART; macOS has no kernel log to follow and
no `/proc/diskstats`, so those two stay off.  It runs as root because
smartctl needs that to reach the drives.  Install the binary, the token
and a state directory, then the daemon:

```
  $ make
  $ sudo install -m 755 drivelist /usr/local/bin/drivelist
  $ sudo mkdir -p /usr/local/etc/drivelist /usr/local/var/drivelist
  $ sudo sh -c 'umask 077; echo agent-secret > /usr/local/etc/drivelist/agent-token'
  $ brew install smartmontools
  $ sudo install -o root -g wheel -m 644 packaging/net.scottstuff.drivelist-agent.plist /Library/LaunchDaemons/
  $ sudo sed -i '' 's/fleet:9450/mgmt1:9450/' /Library/LaunchDaemons/net.scottstuff.drivelist-agent.plist
  $ sudo launchctl bootstrap system /Library/LaunchDaemons/net.scottstuff.drivelist-agent.plist
  $ tail -f /usr/local/var/drivelist/agent.log
```

`sudo launchctl bootout system/net.scottstuff.drivelist-agent` stops
it; edit the plist and bootstrap it again to change a setting.  The
plain `drivelist` listing on that Mac finds the server's statuses at
`/var/lib/drivelist/status.json` by default, so point it at the
daemon's copy with `DRIVELIST_STATUS_CACHE=/usr/local/var/drivelist/status.json`.

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
  $ drivelist admin rebuild      # recompute history from the stored snapshots
```

The server exposes Prometheus metrics at `/metrics` without a token:
drives, missing drives, ghosts, staleness and last report time per
host; known drives by status; kernel warnings and events in the last
24 hours; and report counts and ingest latency.  Per-drive values are
not exported; they live in the database and the CLI.

A drive is referred to by serial, WWN, or an unambiguous prefix of
either.  If one physical drive ends up with two records (seen once
without its WWN, say), `drivelist drive REF merge OTHER` folds OTHER's
history into REF and keeps OTHER's identity resolving to it; if a host
is reinstalled and comes back with a new machine id, `drivelist host
merge INTO FROM` does the same for hosts, with machine ids from
`hosts --ids` when two share a name.  Every command takes `--json` for the raw response.  A drive
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
