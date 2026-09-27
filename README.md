# drivelist

drivelist keeps track of the hardware that wears out in a small
network: **drives**, **memory modules** and **network optics**. For
each one it knows what it is (model and serial), where it is (host,
enclosure and bay, memory slot, or network port), what it is doing,
and how its health is trending. It records every time something
appears, moves, disappears or starts to fail, so you can answer
"where is serial VJG24UZX, where has it been, and is it getting
worse?"

It is built for one person running somewhere between a handful and a
few dozen Linux hosts, a few of them with shelves of disks.

**[See the demo](https://scottlaird.github.io/drivelist/)**: a
read-only snapshot of a real fleet, with the names changed.

## What it tracks

### Drives

- **Where each drive is.** Enclosure and bay for SAS and SATA drives
  behind SES backplanes and JBOD shelves; slot for U.2 and M.2 NVMe
  drives; port for drives on the motherboard's SATA ports. Built-in
  hardware profiles translate firmware slot names into what the
  chassis manual calls each bay.
- **What each drive is used for.** ZFS pool and vdev membership
  (including spares, log, cache and special devices) and mounts, so
  installed-but-unused drives stand out.
- **Health.** SMART readings every six hours, drive errors in the
  kernel log classified and counted per hour, and I/O latency compared
  with the other drives in the same vdev, so a drive three times
  slower than its siblings shows up.
- **SAS links.** The HBA and expander topology, link rates, and the
  per-phy error counters that point at a bad cable or backplane lane.
- **History.** When each drive first appeared, every move between
  bays and hosts, and when it went missing. You can mark a drive
  suspect, bad, shelved or retired, and add notes.

### Memory

- **Every DIMM**, by the slot name printed on the board, with its
  size, speed, part and serial number.
- **Error counts per slot.** The kernel's corrected and uncorrected
  ECC error counts are matched to the physical slot, so a failing
  module is named rather than described as `mc0/csrow3/ch1`.
- **Sanity checks.** Memory is totalled three ways (by module, by the
  kernel's error-counting driver, and by what the OS actually sees),
  which catches missing modules and ranks the BIOS quietly disabled.
  ECC is checked for being fitted but not turned on.

### Network optics

- **Every SFP, QSFP, QSFP-DD and OSFP module**, optical or copper, by
  vendor, part and serial, in whichever port and host it is in now.
- **Light levels per lane**, temperature, and laser bias current,
  checked against the module's own alarm thresholds.
- **Link flaps.** Many optics fail by flapping for a while and then
  running clean for hours; a bout stays flagged for a day.

### Hosts

Reboots and crashes (by boot id, so a crash that recovered before
anyone noticed still shows up), memory and machine-check errors, and
hosts that stop reporting.

## What it needs

- **A server.** One `drivelist serve` process with a SQLite database.
  It serves the agents, the command line, a read-only web interface
  and Prometheus metrics. Any small always-on Linux host will do.
- **An agent on each host, running as root.** Root is needed to run
  `smartctl` and `ethtool -m`, read the firmware's memory tables, and
  follow the kernel log. The agent reports every five minutes and
  spools reports while the server is unreachable.
- **A few standard tools on each host:** `smartctl` (smartmontools 7
  or later) for SMART, `ethtool` for optics, and `zpool` if you use
  ZFS. Each feature quietly switches off if its tool is missing.

Linux is fully supported. A Mac can run the agent and report its
drives and SMART data. A host you would rather not give a token to
can be read over ssh instead (see
[Hosts without an agent](#hosts-without-an-agent)).

## Installing

### Packages

Every [release](https://github.com/scottlaird/drivelist/releases)
has `.deb` packages for `amd64`, `arm64` and `armhf` (32-bit
Raspberry Pi OS). The package pulls in `ethtool` and recommends
`smartmontools`. It installs an agent service, a disabled server
service, and `/etc/default/drivelist`.

### The server

Create two tokens, one for agents and one for you, plus an optional
read-only one for the web interface:

```
sudo mkdir -p /etc/drivelist
for t in agent operator viewer; do openssl rand -hex 32 | sudo tee /etc/drivelist/$t-token >/dev/null; done
sudo chmod 600 /etc/drivelist/*-token
sudo systemctl enable --now drivelist-server
```

The server listens on port 9450. The web interface is at
`http://SERVER:9450/`, and Prometheus metrics at `/metrics`.

### The agent

On each Linux host, install the package, set `DRIVELIST_SERVER` in
`/etc/default/drivelist`, copy the agent token to
`/etc/drivelist/agent-token`, and start it:

```
sudo systemctl start drivelist-agent
```

The service stays idle until the token file exists.

On macOS, build with `make` and run the agent from launchd using
`packaging/net.scottstuff.drivelist-agent.plist`:

```
make
sudo install -m 755 drivelist /usr/local/bin/drivelist
sudo mkdir -p /usr/local/etc/drivelist /usr/local/var/drivelist
sudo sh -c 'umask 077; echo AGENT-TOKEN > /usr/local/etc/drivelist/agent-token'
brew install smartmontools
sudo install -o root -g wheel -m 644 packaging/net.scottstuff.drivelist-agent.plist /Library/LaunchDaemons/
sudo launchctl bootstrap system /Library/LaunchDaemons/net.scottstuff.drivelist-agent.plist
```

Edit the plist's `DRIVELIST_SERVER` first. macOS has no kernel log or
`/proc/diskstats`, so kernel errors and I/O statistics are not
collected there.

### Building from source

drivelist is pure Go with no C dependencies. You need the Go version
in `go.mod`:

```
git clone https://github.com/scottlaird/drivelist.git
cd drivelist
make            # ./drivelist for this machine
make deb        # .deb packages in dist/ (needs nfpm)
```

## Using it

### The web interface

Open `http://SERVER:9450/` and give it the viewer token once. It has a
fleet summary, then pages for hosts, drives, enclosures, SMART,
memory, optics, I/O, SAS errors, events and missing drives, each a sortable table
with a column picker. Every host, drive, enclosure and optic links to
its own page. A host's SAS page draws its topology down to the bays.
The interface is read-only; marking and notes happen on the command
line.

### The command line

Point the CLI at the server once, in `~/.config/drivelist/config`:

```
server = mgmt1:9450
operator_token = OPERATOR-TOKEN
```

The listings, each sorted with problems first:

```
drivelist hosts                   # every host, its agent version and uptime
drivelist drives [--host H]       # every drive and where it is
drivelist smart --problems        # drives whose SMART says something is wrong
drivelist missing                 # drives that vanished and are not marked bad
drivelist io compare              # drives slower than their vdev's median
drivelist dimms --problems        # memory modules with ECC errors
drivelist optics --problems       # optics out of limits or flapping
drivelist events --since 24h      # everything that happened
```

Every listing takes `--fields a,b,c` to choose columns,
`--allfields` for all of them, `--sort a,-b` to sort, and `--json`.

One drive or optic, by serial (or any unambiguous prefix of one):

```
drivelist drive VJG24UZX                  # where it is now, its status
drivelist drive VJG24UZX history          # everything that has happened to it
drivelist drive VJG24UZX smart            # its SMART readings
drivelist drive VJG24UZX mark suspect --note "3x slower than siblings"
drivelist drive VJG24UZX note "RMA 4471 opened"
drivelist optic MT1918FT01234             # every lane, its thresholds, where it has been
drivelist optic sw1:swp4 history          # hourly levels and flaps; HOST:PORT works too
```

Marking a drive `bad`, `shelved` or `retired` means its absence is
expected, so it drops out of `missing`.

### Enclosures and bays

`drivelist enclosures` lists every shelf, backplane and chassis the
agents have seen. Give one a name that every listing will use:

```
drivelist enclosure 0x5000ccab020094ff name "garage shelf"
drivelist enclosure 0x5000ccab020094ff bays      # bay by bay, in the chassis layout
```

Bays are identified by what the firmware reports, which rarely
matches the chassis label. `hardware/profiles/` holds profiles that
map one to the other for specific chassis, and they are built into
the binary. `drivelist hardware check HOST` shows what a new profile
would need to cover. Contributions are welcome.

### Without a server

`drivelist` on its own lists the drives in this host, with what each
is used for:

```
$ sudo drivelist
Device Name     Model                   WWN                     Serial                  Expander        Bay     Size
===========     =====                   ===                     ======                  ========        ===     ====
sda             HUH721008AL5204         0x5000cca25206c808      7SG3RM2G                expander-4:0    0       8 TB
sdaa            H7280A520SUN8.0T        0x5000cca2548eecec      001619PJLREV_VKJJLREV   expander-11:0   54      8 TB
...
```

`--unused` shows only drives in no pool and no mount, which is how you
find the drive left behind after a migration.
`drivelist sas --errors` shows the local SAS phys with error counts,
and `drivelist optics --local` the local optics.

### Hosts without an agent

A host you would rather not trust with a token can be read over ssh
from a host that has the operator token. The report includes SMART
and I/O counters:

```
ssh web1 sudo drivelist report --output - --smart | drivelist admin ingest -q -
```

Run it from cron every hour and the host gets hourly I/O buckets like
any other. It misses only the agent's kernel-log watch.

### Metrics

`/metrics` needs no token. It exports per-host counts, drives by
status, per-phy SAS error counters, and each optic's temperature,
per-lane power and bias, flap counter and problem state.

### A demo of your own

`drivelist demo export DIR --names demo/names.json` writes a static,
sanitized copy of the web interface: hosts renamed or numbered, and
strings rewritten, as the names file says. `demo/publish.sh DIR`
pushes it to a `gh-pages` branch. Review `DIR/data` first: notes,
kernel messages and serial numbers are published as they are.

## How some of it works

**Locating drives.** A SAS or SATA drive's place is the SES enclosure
and bay its end device reports, so it survives reboots that renumber
the kernel's expanders. An NVMe drive's place is the chassis and the
PCIe slot, taken from the hotplug slot table, else the board's SMBIOS
slot names, else the root port address. An onboard SATA drive's place
is the ATA port.

**Matching errors to DIMM slots.** The kernel reports memory errors by
memory controller, channel and chip select. The firmware's bank
locator (`P0_Node0_Channel1_Dimm1`) settles which slot that is when it
names the channel. Otherwise the slot name's channel letter does, and
that match is exact when a channel holds one module. `drivelist dimms
--allfields` shows how sure each match is.

**Optic alarms.** Module alarm flags latch, and some NIC firmware never
clears them, so a flag counts only when the module's current reading
bears it out. On a port whose link is down, low light is expected and
not flagged.

**A kernel bug.** Linux 6.18.38–39, 7.1.3–4 and distribution kernels
with the same patch (such as Ubuntu's 7.0.0-31) sometimes add the
host's whole uptime to the disk I/O time counters. The agent drops the
latency of any minute where that happens. `drivelist drive REF io`
counts the dropped minutes.

## Limitations

- Linux MD, LVM and btrfs are not recognised as uses.
- USB drives probably work but are untested.
- Optics are read from Linux hosts running the agent; switches running
  other operating systems are not polled.
- A module `ethtool -m` cannot read is treated as an empty port.

## Development

The collector's inputs (sysfs, `/proc`, and the output of `udevadm`,
`zpool`, `smartctl` and `ethtool`) are injectable. `collect/testdata/`
holds trees captured from real systems, which the tests run against
on any OS. To capture one:

```
sudo drivelist capture /tmp/myhost
```

The capture includes every drive's serial number and WWN. Each tree
has golden files of what the collector produces from it; after an
intended change, regenerate them with `go test ./collect -update` and
review the diff.
