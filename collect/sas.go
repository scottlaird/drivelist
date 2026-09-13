package collect

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// SASTopology is what the SAS transport class in sysfs says about a host's
// HBAs, the expanders behind them, and the phys that link them: one node
// per HBA or expander, in discovery order (each HBA, then the expanders
// behind it, nearest first).
type SASTopology struct {
	Nodes []*SASNode
}

// SASNode is an HBA or an expander: something with phys of its own.
type SASNode struct {
	Kind     string // "hba" or "expander"
	Name     string // "host4", "expander-4:0"
	Address  string // its SAS address
	Vendor   string // expander vendor_id; the HBA's driver
	Product  string // expander product_id; the HBA's board_name
	Revision string // expander product_rev; the HBA's firmware version
	Parent   string // the node upstream, "" for an HBA
	Upstream string // the upstream node's port that leads here, "" for an HBA
	Phys     []*SASPhy
	Ports    []*SASPort
	Devices  []*SASEndDevice // end devices attached to this node's ports
	Path     string          // sysfs directory
}

// SASPhy is one phy of an HBA or expander: its link and the SAS-level
// error counters the kernel keeps for it, cumulative since boot.
type SASPhy struct {
	Name          string // "phy-4:0:5"
	ID            int    // phy_identifier
	Port          string // the port it belongs to, "" when nothing is attached
	Rate          string // negotiated_linkrate as the kernel prints it: "12.0 Gbit", "Unknown", "Phy disabled"
	MaxRate       string // maximum_linkrate: what it is allowed to negotiate
	MaxRateHW     string // maximum_linkrate_hw: what it could
	Enabled       bool
	InvalidDword  uint64 // invalid_dword_count
	DisparityErr  uint64 // running_disparity_error_count
	LossDwordSync uint64 // loss_of_dword_sync_count
	ResetProblem  uint64 // phy_reset_problem_count
}

// Gbit is the negotiated rate as a number, 0 without a link.
func (p *SASPhy) Gbit() float64 { return linkGbit(p.Rate) }

// Up reports whether the phy has a link.
func (p *SASPhy) Up() bool { return p.Gbit() > 0 }

// Errors is the sum of the four counters.
func (p *SASPhy) Errors() uint64 {
	return p.InvalidDword + p.DisparityErr + p.LossDwordSync + p.ResetProblem
}

// SASPort is a port of an HBA or expander: one or more phys bundled to
// reach one attached device. A wide port has several.
type SASPort struct {
	Name     string   // "port-4:0", "port-4:0:1"
	Phys     []string // phy names, sorted
	NumPhys  int      // what sysfs claims; equals len(Phys) unless a phy is missing
	Attached string   // the node or end device behind it: "expander-4:0", "end_device-4:0:1"
}

// SASEndDevice is a drive (or anything else that is not an expander) on
// a port.
type SASEndDevice struct {
	Name      string // "end_device-4:0:1"
	Address   string
	Port      string // the port on the parent node
	Bay       string // bay_identifier, "" if none
	Enclosure string // enclosure_identifier
	Protocols string // target_port_protocols: "ssp" for SAS, "stp" for SATA behind an expander
	DevName   string // block device, "" if none (an empty bay, or not a disk)
}

// Node returns the node named, or nil.
func (t *SASTopology) Node(name string) *SASNode {
	for _, n := range t.Nodes {
		if n.Name == name {
			return n
		}
	}
	return nil
}

// Port returns the node's port named, or nil.
func (n *SASNode) Port(name string) *SASPort {
	for _, p := range n.Ports {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// PhysOf returns the phys in a port, in name order.
func (n *SASNode) PhysOf(port string) []*SASPhy {
	var out []*SASPhy
	for _, p := range n.Phys {
		if p.Port == port && port != "" {
			out = append(out, p)
		}
	}
	return out
}

// DevicePhys maps each block device to the phys that reach it: the phys
// of the port on its nearest node. Upstream links are not included; see
// PathTo for those.
func (t *SASTopology) DevicePhys() map[string][]*SASPhy {
	out := map[string][]*SASPhy{}
	for _, n := range t.Nodes {
		for _, d := range n.Devices {
			if d.DevName != "" {
				out[d.DevName] = n.PhysOf(d.Port)
			}
		}
	}
	return out
}

// SAS reads the SAS topology of the live system or fixture. A host with
// no SAS transport (NVMe only, plain AHCI) gets an empty topology and no
// error.
func (c *Collector) SAS() (*SASTopology, error) {
	t := &SASTopology{}
	classDir := filepath.Join(c.sys(), "class", "sas_host")
	entries, err := os.ReadDir(classDir)
	if err != nil {
		if os.IsNotExist(err) {
			return t, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Slice(names, func(i, j int) bool { return scsiHostLess(names[i], names[j]) })
	for _, name := range names {
		target, err := filepath.EvalSymlinks(filepath.Join(classDir, name))
		if err != nil {
			slog.Warn("sas: host link unreadable", "host", name, "err", err)
			continue
		}
		// /sys/class/sas_host/host4 -> …/host4/sas_host/host4; the device
		// directory is two levels up.
		hostDir := filepath.Dir(filepath.Dir(target))
		if err := t.readNode(hostDir, "hba", "", ""); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// readNode reads an HBA or expander directory and, through its ports,
// everything behind it.
func (t *SASTopology) readNode(dir, kind, parent, upstream string) error {
	name := filepath.Base(dir)
	n := &SASNode{Kind: kind, Name: name, Parent: parent, Upstream: upstream, Path: dir}
	switch kind {
	case "hba":
		sh := filepath.Join(dir, "scsi_host", name)
		n.Address = sysAttr(sh, "host_sas_address")
		n.Vendor = sysAttr(sh, "proc_name")
		n.Product = sysAttr(sh, "board_name")
		n.Revision = sysAttr(sh, "version_fw")
	case "expander":
		sd := filepath.Join(dir, "sas_device", name)
		se := filepath.Join(dir, "sas_expander", name)
		n.Address = sysAttr(sd, "sas_address")
		n.Vendor = sysAttr(se, "vendor_id")
		n.Product = sysAttr(se, "product_id")
		n.Revision = sysAttr(se, "product_rev")
	}
	t.Nodes = append(t.Nodes, n)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var portDirs []string
	for _, e := range entries {
		switch {
		case strings.HasPrefix(e.Name(), "phy-") && e.IsDir():
			n.Phys = append(n.Phys, readPhy(filepath.Join(dir, e.Name())))
		case strings.HasPrefix(e.Name(), "port-") && e.IsDir():
			portDirs = append(portDirs, filepath.Join(dir, e.Name()))
		}
	}
	sort.Slice(n.Phys, func(i, j int) bool { return n.Phys[i].ID < n.Phys[j].ID })
	sort.Slice(portDirs, func(i, j int) bool { return sasNameLess(filepath.Base(portDirs[i]), filepath.Base(portDirs[j])) })
	for _, pd := range portDirs {
		if err := t.readPort(n, pd); err != nil {
			return err
		}
	}
	return nil
}

// readPort reads a port directory: which phys it bundles (symlinks named
// after them) and the one expander or end device behind it.
func (t *SASTopology) readPort(n *SASNode, dir string) error {
	port := &SASPort{Name: filepath.Base(dir)}
	if v, err := strconv.Atoi(sysAttr(filepath.Join(dir, "sas_port", port.Name), "num_phys")); err == nil {
		port.NumPhys = v
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var child string
	for _, e := range entries {
		switch {
		case strings.HasPrefix(e.Name(), "phy-") && e.Type()&fs.ModeSymlink != 0:
			port.Phys = append(port.Phys, e.Name())
		case strings.HasPrefix(e.Name(), "expander-") && e.IsDir(),
			strings.HasPrefix(e.Name(), "end_device-") && e.IsDir():
			child = e.Name()
		}
	}
	sort.Slice(port.Phys, func(i, j int) bool { return sasNameLess(port.Phys[i], port.Phys[j]) })
	for _, p := range n.Phys {
		for _, name := range port.Phys {
			if p.Name == name {
				p.Port = port.Name
			}
		}
	}
	port.Attached = child
	n.Ports = append(n.Ports, port)
	switch {
	case strings.HasPrefix(child, "expander-"):
		return t.readNode(filepath.Join(dir, child), "expander", n.Name, port.Name)
	case strings.HasPrefix(child, "end_device-"):
		n.Devices = append(n.Devices, readEndDevice(filepath.Join(dir, child), port.Name))
	}
	return nil
}

func readPhy(dir string) *SASPhy {
	name := filepath.Base(dir)
	a := filepath.Join(dir, "sas_phy", name)
	p := &SASPhy{Name: name, Rate: sysAttr(a, "negotiated_linkrate"), MaxRate: sysAttr(a, "maximum_linkrate"), MaxRateHW: sysAttr(a, "maximum_linkrate_hw")}
	p.ID, _ = strconv.Atoi(sysAttr(a, "phy_identifier"))
	p.Enabled = sysAttr(a, "enable") != "0"
	p.InvalidDword = sysCounter(a, "invalid_dword_count")
	p.DisparityErr = sysCounter(a, "running_disparity_error_count")
	p.LossDwordSync = sysCounter(a, "loss_of_dword_sync_count")
	p.ResetProblem = sysCounter(a, "phy_reset_problem_count")
	return p
}

func readEndDevice(dir, port string) *SASEndDevice {
	name := filepath.Base(dir)
	a := filepath.Join(dir, "sas_device", name)
	d := &SASEndDevice{Name: name, Port: port, Address: sysAttr(a, "sas_address"), Bay: sysAttr(a, "bay_identifier"),
		Enclosure: sysAttr(a, "enclosure_identifier"), Protocols: sysAttr(a, "target_port_protocols")}
	// The block device, if any, is at target<h:c:i>/<h:c:i:l>/block/<name>.
	targets, _ := filepath.Glob(filepath.Join(dir, "target*", "*", "block", "*"))
	for _, tdir := range targets {
		if IsDiskName(filepath.Base(tdir)) {
			d.DevName = filepath.Base(tdir)
			break
		}
	}
	return d
}

// sysAttr reads one sysfs attribute, "" when it is absent or unreadable
// (some attributes exist but return EIO on some hardware).
func sysAttr(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func sysCounter(dir, name string) uint64 {
	v, _ := strconv.ParseUint(sysAttr(dir, name), 10, 64)
	return v
}

// linkGbit turns the kernel's link rate string into Gbit/s: "12.0 Gbit" is
// 12; anything without a number (Unknown, Phy disabled, Spin-up hold) is
// 0.
func linkGbit(rate string) float64 {
	f, _, ok := strings.Cut(rate, " ")
	if !ok {
		return 0
	}
	v, err := strconv.ParseFloat(f, 64)
	if err != nil {
		return 0
	}
	return v
}

// sasNameLess orders sysfs SAS names numerically by their H:N:M parts, so
// phy-4:0:10 follows phy-4:0:9.
func sasNameLess(a, b string) bool {
	pa, pb := sasNumbers(a), sasNumbers(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

func sasNumbers(name string) []int {
	_, rest, _ := strings.Cut(name, "-")
	var out []int
	for _, f := range strings.Split(rest, ":") {
		v, _ := strconv.Atoi(f)
		out = append(out, v)
	}
	return out
}

func scsiHostLess(a, b string) bool {
	va, _ := strconv.Atoi(strings.TrimPrefix(a, "host"))
	vb, _ := strconv.Atoi(strings.TrimPrefix(b, "host"))
	return va < vb
}

// sasAttrs are the attributes Capture copies from each SAS class
// directory, by class. Missing ones are skipped.
var sasAttrs = map[string][]string{
	"sas_phy":        {"phy_identifier", "sas_address", "negotiated_linkrate", "minimum_linkrate", "maximum_linkrate", "minimum_linkrate_hw", "maximum_linkrate_hw", "enable", "device_type", "initiator_port_protocols", "target_port_protocols", "invalid_dword_count", "running_disparity_error_count", "loss_of_dword_sync_count", "phy_reset_problem_count"},
	"sas_port":       {"num_phys"},
	"sas_device":     {"sas_address", "device_type", "phy_identifier", "scsi_target_id", "bay_identifier", "enclosure_identifier", "initiator_port_protocols", "target_port_protocols"},
	"sas_expander":   {"vendor_id", "product_id", "product_rev", "component_vendor_id", "component_id", "component_revision_id", "level"},
	"sas_end_device": {"ready_led_meaning", "tlr_supported", "tlr_enabled"},
	"sas_host":       {"uevent"}, // so the class symlink has something to resolve to
	"scsi_host":      {"host_sas_address", "board_name", "version_fw", "version_bios", "version_mpi", "proc_name", "unique_id"},
}

// captureSAS copies the SAS transport trees of every SAS host into the
// fixture: the class attributes above for each phy, port, expander, end
// device and host, and the phy symlinks in each port directory, which is
// how sysfs says which phys a port bundles. The class symlink for each
// host is recreated so SAS finds the hosts the same way on a fixture.
func (c *Collector) captureSAS(dir string) error {
	classDir := filepath.Join(c.sys(), "class", "sas_host")
	entries, err := os.ReadDir(classDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		link, err := os.Readlink(filepath.Join(classDir, e.Name()))
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, "sys", "class", "sas_host", e.Name())
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		os.Remove(dst)
		if err := os.Symlink(link, dst); err != nil {
			return err
		}
		target, err := filepath.EvalSymlinks(filepath.Join(classDir, e.Name()))
		if err != nil {
			return err
		}
		hostDir := filepath.Dir(filepath.Dir(target))
		if err := c.captureSASTree(dir, hostDir); err != nil {
			return fmt.Errorf("capture %s: %w", e.Name(), err)
		}
	}
	return nil
}

func (c *Collector) captureSASTree(dir, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel := strings.TrimPrefix(path, c.sys())
		base := filepath.Base(path)
		parent := filepath.Base(filepath.Dir(path))
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			// A phy symlink in a port directory: keep it as a symlink.
			if strings.HasPrefix(base, "phy-") && strings.HasPrefix(parent, "port-") {
				link, err := os.Readlink(path)
				if err != nil {
					return nil
				}
				dst := filepath.Join(dir, "sys", filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return err
				}
				os.Remove(dst)
				return os.Symlink(link, dst)
			}
			return nil
		case d.IsDir():
			// Descend only through SAS transport names, the class
			// subdirectories, and the target path down to the block device.
			if path == root || strings.HasPrefix(parent, "sas_") || parent == "scsi_host" {
				return nil // the host itself, or a class instance directory
			}
			for _, p := range []string{"phy-", "port-", "expander-", "end_device-", "target", "sas_", "scsi_host", "block"} {
				if strings.HasPrefix(base, p) {
					return nil
				}
			}
			if _, err := strconv.Atoi(strings.ReplaceAll(base, ":", "")); err == nil && strings.HasPrefix(parent, "target") {
				return nil // the h:c:i:l directory under a target
			}
			if parent == "block" {
				return nil // the disk directory itself, for its name
			}
			return fs.SkipDir
		}
		// A file: copy it if its directory is a class directory we want.
		class := filepath.Base(filepath.Dir(filepath.Dir(path)))
		if attrs, ok := sasAttrs[class]; ok {
			for _, a := range attrs {
				if a == base {
					if err := c.copySys(dir, rel); err != nil {
						slog.Debug("capture: sas attribute", "path", path, "err", err)
					}
					return nil
				}
			}
		}
		return nil
	})
}
