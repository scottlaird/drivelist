package collect

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// captureCommands are run verbatim during Capture and their output saved,
// so that fixtures carry everything a future collector might parse, not
// only what the current one does. Failures are logged and skipped: not
// every host has every command or every flag.
var captureCommands = [][]string{
	{"zpool", "version"},
	{"zpool", "status", "-P"},
	{"zpool", "status", "-g"},
	{"zpool", "status", "-jP"},
	{"zpool", "list", "-HPv"},
	{"zpool", "get", "-Hp", "guid"},
}

// Capture writes everything Collect reads from this system into dir, in
// the layout Fixture expects: the sysfs files under dir/sys, mountinfo under
// dir/proc, and command output under dir/exec. Run it on a real host to
// produce a test fixture for that host's configuration.
//
// Only the parts of sysfs the collector touches are copied: the block
// device list (as each device's dev file), each device's size, and every
// bay_identifier under each expander. Sysfs symlinks are followed and
// written as directories.
func (c *Collector) Capture(dir string) error {
	names, err := c.diskNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := c.copySys(dir, "/block/"+name+"/dev"); err != nil {
			return fmt.Errorf("capture %s: %w", name, err)
		}
		out, err := c.run("udevadm", "info", "--query=all", "--name=/dev/"+name)
		if err != nil {
			// Same outcome as Collect: the device is listed but unidentified.
			slog.Warn("capture: udevadm failed, device recorded without identity", "device", name, "err", err)
			continue
		}
		if err := c.captureExec(dir, out, "udevadm", "info", "--query=all", "--name=/dev/"+name); err != nil {
			return err
		}
		u, err := parseUdevInfo(out)
		if err != nil {
			return err
		}
		devpath := u.Attribs["DEVPATH"]
		if err := c.copySys(dir, devpath+"/size"); err != nil {
			slog.Warn("capture: size", "device", name, "err", err)
		}
		if exp := expanderPath(devpath); exp != "" {
			if err := c.copyBayIdentifiers(dir, exp); err != nil {
				return err
			}
		}
	}

	if err := c.copyFile(c.proc()+"/self/mountinfo", filepath.Join(dir, "proc", "self", "mountinfo")); err != nil {
		return err
	}

	for _, argv := range captureCommands {
		out, err := c.run(argv[0], argv[1:]...)
		if err != nil {
			slog.Warn("capture: command failed, skipped", "argv", strings.Join(argv, " "), "err", err)
			continue
		}
		if err := c.captureExec(dir, out, argv[0], argv[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func (c *Collector) captureExec(dir string, out []byte, name string, args ...string) error {
	path := filepath.Join(dir, "exec", execKey(name, args))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// expanderPath returns the sysfs path (relative to the sysfs root) of the
// expander a DEVPATH hangs off, or "" if there is none.
func expanderPath(devpath string) string {
	var prefix string
	for i, p := range strings.Split(devpath, "/") {
		if i > 0 {
			prefix += "/"
		}
		prefix += p
		if strings.HasPrefix(p, "expander-") {
			return prefix
		}
	}
	return ""
}

// copyBayIdentifiers copies every bay_identifier under an expander's sysfs
// subtree, skipping the expander's own (reading it fails on real systems).
func (c *Collector) copyBayIdentifiers(dir, exp string) error {
	root := c.sys() + exp
	return filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil || filepath.Base(path) != "bay_identifier" {
			return nil
		}
		if isExpanderOwnBay(path) {
			return nil
		}
		rel := strings.TrimPrefix(path, c.sys())
		if err := c.copySys(dir, rel); err != nil {
			slog.Warn("capture: bay_identifier", "path", path, "err", err)
		}
		return nil
	})
}

// copySys copies one file from the sysfs root into dir/sys at the same
// relative path.
func (c *Collector) copySys(dir, rel string) error {
	return c.copyFile(c.sys()+rel, filepath.Join(dir, "sys", filepath.FromSlash(rel)))
}

func (c *Collector) copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
