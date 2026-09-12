package collect

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/scottlaird/drivelist"
)

// mountEntry is one line of /proc/self/mountinfo, reduced to the fields
// drivelist cares about.
type mountEntry struct {
	Source     string // the device or pseudo-source, e.g. /dev/sda1
	Mountpoint string
	FSType     string
}

// annotateMounts adds a "mount > <mountpoint>" use to every disk that backs
// a mounted filesystem, matching by device path (partitions resolve to
// their parent disk through Inventory.ByName).
func annotateMounts(inv *drivelist.Inventory) error {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	defer f.Close()

	mounts, err := parseMountInfo(f)
	if err != nil {
		return err
	}

	for _, mount := range mounts {
		slog.Debug("mount", "source", mount.Source, "mountpoint", mount.Mountpoint)
		disk := inv.ByName(mount.Source)
		if disk != nil {
			disk.Uses = append(disk.Uses, "mount > "+mount.Mountpoint)
		}
	}
	return nil
}

// parseMountInfo reads the /proc/[pid]/mountinfo format described in
// proc(5): fixed fields, zero or more optional fields, a "-" separator,
// then filesystem type, source and super options. Mount points and
// sources are unescaped.
func parseMountInfo(r io.Reader) ([]mountEntry, error) {
	var entries []mountEntry
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		pre, post, ok := strings.Cut(line, " - ")
		if !ok {
			return nil, fmt.Errorf("mountinfo: no separator in %q", line)
		}
		preFields := strings.Fields(pre)
		postFields := strings.Fields(post)
		if len(preFields) < 6 || len(postFields) < 2 {
			return nil, fmt.Errorf("mountinfo: too few fields in %q", line)
		}
		entries = append(entries, mountEntry{
			Mountpoint: unescapeMountField(preFields[4]),
			FSType:     postFields[0],
			Source:     unescapeMountField(postFields[1]),
		})
	}
	return entries, scanner.Err()
}

// unescapeMountField decodes the \ooo octal escapes the kernel uses for
// space, tab, newline and backslash in mountinfo fields.
func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
