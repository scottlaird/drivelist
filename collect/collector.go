package collect

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/scottlaird/drivelist"
)

// Collector gathers an Inventory from a set of sources. The zero value reads
// the live system; tests and captures point the fields at a fixture tree
// (see Fixture and Capture).
type Collector struct {
	// Platform selects the collection path: "linux" (sysfs, udev, zpool)
	// or "darwin" (diskutil, system_profiler). Empty means runtime.GOOS. A
	// fixture carries its platform, so a Mac's capture collects on Linux.
	Platform string
	// Sys is the root of sysfs. Empty means /sys.
	Sys string
	// Proc is the root of procfs. Empty means /proc.
	Proc string
	// Exec runs a command and returns its standard output. nil means
	// os/exec with the command found on PATH.
	Exec func(name string, args ...string) ([]byte, error)
	// ZFS annotates pool membership. nil means the default: run zpool
	// through Exec (or libzfs, in builds with the libzfs tag).
	ZFS func(*drivelist.Inventory) error
}

func (c *Collector) platform() string {
	if c.Platform == "" {
		return runtime.GOOS
	}
	return c.Platform
}

// All collects from the live system with default settings.
func All() (*drivelist.Inventory, error) {
	return (&Collector{}).Collect()
}

func (c *Collector) sys() string {
	if c.Sys == "" {
		return "/sys"
	}
	return c.Sys
}

func (c *Collector) proc() string {
	if c.Proc == "" {
		return "/proc"
	}
	return c.Proc
}

func (c *Collector) run(name string, args ...string) ([]byte, error) {
	if c.Exec != nil {
		return c.Exec(name, args...)
	}
	return exec.Command(name, args...).Output()
}

// Collect enumerates every disk and annotates each with its uses (ZFS pool
// membership, mounts) and, on Linux, where the enclosure reports it, its
// physical bay; empty enclosure bays are appended as devices with no name.
// Platforms other than Linux and macOS get ErrUnsupported.
func (c *Collector) Collect() (*drivelist.Inventory, error) {
	switch c.platform() {
	case "linux":
		return c.collectLinux()
	case "darwin":
		return c.collectDarwin()
	}
	return nil, ErrUnsupported
}

func (c *Collector) collectLinux() (*drivelist.Inventory, error) {
	inv, err := c.disks()
	if err != nil {
		return inv, err
	}
	for _, annotate := range []func(*drivelist.Inventory) error{
		c.zfsAnnotator(),
		c.annotateMounts,
		annotateMD,
		annotateLVM,
		c.annotateEmptyBays,
	} {
		if err := annotate(inv); err != nil {
			return inv, err
		}
	}
	return inv, nil
}

// zfsAnnotator is the ZFS hook, or the platform default when unset.
func (c *Collector) zfsAnnotator() func(*drivelist.Inventory) error {
	if c.ZFS != nil {
		return c.ZFS
	}
	return c.annotateZFS
}

// Fixture returns a Collector that reads a captured tree instead of the
// live system: sysfs under dir/sys, procfs under dir/proc, and command
// output from dir/exec/<command line> as written by Capture. The tree's
// platform file says which path to take; an old tree without one is
// Linux. Pool membership comes from the captured zpool output whatever
// the build tag; a tree without it has no pools.
func Fixture(dir string) *Collector {
	c := &Collector{
		Platform: "linux",
		Sys:      filepath.Join(dir, "sys"),
		Proc:     filepath.Join(dir, "proc"),
		Exec: func(name string, args ...string) ([]byte, error) {
			return os.ReadFile(filepath.Join(dir, "exec", execKey(name, args)))
		},
	}
	if b, err := os.ReadFile(filepath.Join(dir, "platform")); err == nil {
		c.Platform = strings.TrimSpace(string(b))
	}
	c.ZFS = c.annotateZFSExec
	return c
}

// execKey is the fixture file name for a command line: the command's base
// name and its arguments joined by spaces, with slashes replaced so the key
// stays a single path component.
func execKey(name string, args []string) string {
	parts := append([]string{filepath.Base(name)}, args...)
	return strings.ReplaceAll(strings.Join(parts, " "), "/", "_")
}
