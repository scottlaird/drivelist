package drivelist

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseMountInfo(t *testing.T) {
	in := `
22 28 0:21 / /proc rw,nosuid,nodev,noexec,relatime shared:13 - proc proc rw
28 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw,errors=remount-ro
101 28 8:17 / /mnt/with\040space rw,relatime shared:50 - xfs /dev/sdb1 rw,attr2
102 28 0:45 / /space rw,xattr,noacl shared:60 master:3 - zfs space rw,xattr,noacl
`
	want := []mountEntry{
		{Source: "proc", Mountpoint: "/proc", FSType: "proc"},
		{Source: "/dev/sda1", Mountpoint: "/", FSType: "ext4"},
		{Source: "/dev/sdb1", Mountpoint: "/mnt/with space", FSType: "xfs"},
		{Source: "space", Mountpoint: "/space", FSType: "zfs"},
	}
	got, err := parseMountInfo(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseMountInfo: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("parseMountInfo mismatch (-want +got):\n%s", diff)
	}
}

func TestParseMountInfoMalformed(t *testing.T) {
	for _, in := range []string{
		"28 1 8:1 / / rw,relatime shared:1 ext4 /dev/sda1 rw", // no separator
		"28 1 8:1 / - ext4 /dev/sda1 rw",                      // too few fields
	} {
		if _, err := parseMountInfo(strings.NewReader(in)); err == nil {
			t.Errorf("parseMountInfo(%q): got nil error, want error", in)
		}
	}
}

func TestUnescapeMountField(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/plain", "/plain"},
		{`/a\040b`, "/a b"},
		{`/tab\011x`, "/tab\tx"},
		{`/back\134slash`, `/back\slash`},
		{`/trailing\04`, `/trailing\04`}, // incomplete escape left alone
	}
	for _, tc := range tests {
		if got := unescapeMountField(tc.in); got != tc.want {
			t.Errorf("unescapeMountField(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
