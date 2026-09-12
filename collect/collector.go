package collect

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/scottlaird/drivelist"
)

// Collector gathers an Inventory from a set of sources. The zero value reads
// the live system; tests and captures point the fields at a fixture tree
// (see Fixture and Capture).
type Collector struct {
	// Sys is the root of sysfs. Empty means /sys.
	Sys string
	// Proc is the root of procfs. Empty means /proc.
	Proc string
	// Exec runs a command and returns its standard output. nil means
	// os/exec with the command found on PATH.
	Exec func(name string, args ...string) ([]byte, error)
	// ZFS annotates pool membership. nil means the platform default: libzfs
	// where it is compiled in, otherwise nothing.
	ZFS func(*drivelist.Inventory) error
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
// membership, mounts) and, where the enclosure reports it, its physical
// bay. Empty enclosure bays are appended as devices with no name.
func (c *Collector) Collect() (*drivelist.Inventory, error) {
	inv, err := c.disks()
	if err != nil {
		return inv, err
	}
	zfs := c.ZFS
	if zfs == nil {
		zfs = annotateZFS
	}
	for _, annotate := range []func(*drivelist.Inventory) error{
		zfs,
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

// Fixture returns a Collector that reads a captured tree instead of the
// live system: sysfs under dir/sys, procfs under dir/proc, and command
// output from dir/exec/<command line> as written by Capture. ZFS
// annotation is disabled; tests set ZFS themselves.
func Fixture(dir string) *Collector {
	return &Collector{
		Sys:  filepath.Join(dir, "sys"),
		Proc: filepath.Join(dir, "proc"),
		Exec: func(name string, args ...string) ([]byte, error) {
			return os.ReadFile(filepath.Join(dir, "exec", execKey(name, args)))
		},
		ZFS: func(*drivelist.Inventory) error { return nil },
	}
}

// execKey is the fixture file name for a command line: the command's base
// name and its arguments joined by spaces, with slashes replaced so the key
// stays a single path component.
func execKey(name string, args []string) string {
	parts := append([]string{filepath.Base(name)}, args...)
	return strings.ReplaceAll(strings.Join(parts, " "), "/", "_")
}
