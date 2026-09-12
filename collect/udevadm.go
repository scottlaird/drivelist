package collect

import (
	"bufio"
	"bytes"
	"log/slog"
	"strings"
	"sync"
)

// udevData is the parsed output of `udevadm info --query=all` for one device.
type udevData struct {
	DeviceName string
	Attribs    map[string]string
}

func (c *Collector) udevInfo(name string) (*udevData, error) {
	data, err := c.run("udevadm", "info", "--query=all", "--name="+name)
	if err != nil {
		return nil, err
	}
	return parseUdevInfo(data)
}

// udevIgnoredRecords are the record types udevadm info prints that carry
// nothing the E: properties do not: P sysfs path, M sysfs name, R sysfs
// number, J device id, U subsystem, T device type, D major:minor, I ifindex,
// L symlink priority, S symlink, Q diskseq, V driver. Newer systemd adds
// types over time; an unknown one is logged once per process rather than
// per device.
const udevIgnoredRecords = "PMRJUTDILSQV"

var unknownUdevRecords sync.Map

// parseUdevInfo reads udevadm's line-per-record output: N: for the kernel
// name, E: for KEY=VALUE properties; other record types are ignored.
func parseUdevInfo(data []byte) (*udevData, error) {
	d := &udevData{Attribs: make(map[string]string)}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		t := scanner.Text()
		if len(t) < 3 {
			continue
		}
		switch {
		case t[0] == 'N':
			d.DeviceName = t[3:]
		case t[0] == 'E':
			key, value, _ := strings.Cut(t[3:], "=")
			d.Attribs[key] = value
		case strings.IndexByte(udevIgnoredRecords, t[0]) >= 0:
		default:
			if _, seen := unknownUdevRecords.LoadOrStore(t[0], true); !seen {
				slog.Warn("unknown udevadm record type; ignoring", "type", string(t[0]), "line", t)
			}
		}
	}
	return d, scanner.Err()
}
