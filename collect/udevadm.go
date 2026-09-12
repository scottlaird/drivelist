package collect

import (
	"bufio"
	"bytes"
	"log/slog"
	"os/exec"
	"strings"
)

// udevData is the parsed output of `udevadm info --query=all` for one device.
type udevData struct {
	DeviceName string
	Attribs    map[string]string
}

func udevInfo(name string) (*udevData, error) {
	data, err := exec.Command("/bin/udevadm", "info", "--query=all", "--name="+name).Output()
	if err != nil {
		return nil, err
	}
	return parseUdevInfo(data)
}

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
		switch t[0] {
		case 'N':
			d.DeviceName = t[3:]
		case 'E':
			key, value, _ := strings.Cut(t[3:], "=")
			d.Attribs[key] = value
		case 'L', 'S', 'P':
			// Link priority, symlinks and sysfs path: all recoverable from E: lines.
		default:
			slog.Warn("unknown udevadm line", "line", t)
		}
	}
	return d, scanner.Err()
}
