package drivelist

import (
	"bufio"
	"bytes"
	"os/exec"
	"strings"

	"github.com/golang/glog"
)

type UdevAdmData struct {
	DeviceName string
	Attribs    map[string]string
}

func GetUdevAdmData(name string) (*UdevAdmData, error) {
	data, err := exec.Command("/bin/udevadm", "info", "--query=all", "--name="+name).Output()
	if err != nil {
		return nil, err
	}

	return parseUdevAdmData(name, data)
}

func parseUdevAdmData(name string, data []byte) (*UdevAdmData, error) {
	d := &UdevAdmData{}
	d.Attribs = make(map[string]string)

	scanner := bufio.NewScanner(bytes.NewReader(data))

	for scanner.Scan() {
		t := scanner.Text()
		if len(t) > 0 {
			switch t[0] {
			case 'N':
				d.DeviceName = t[3:]
			case 'L':
				// Ignore
			case 'S':
				// Ignore
			case 'P':
				// Ignore
			case 'E':
				sp := strings.Split(t[3:], "=")
				d.Attribs[sp[0]] = sp[1]
			default:
				glog.Warningf("Found unknown udevadm line: %s\n", t)
			}
		}

	}

	return d, nil
}
