package collect

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/scottlaird/drivelist"
)

// A pool with every section zpool status can print: regular and special
// vdevs, a replacing pair, a missing leaf, a log mirror, cache devices, and
// spares including one in use under a spare-N vdev.
const statusP = `  pool: tank
 state: DEGRADED
status: One or more devices could not be opened.
config:

	NAME                                  STATE     READ WRITE CKSUM
	tank                                  DEGRADED     0     0     0
	  raidz2-0                            DEGRADED     0     0     0
	    /dev/disk/by-id/wwn-0xa-part1     ONLINE       0     0     0
	    replacing-1                       DEGRADED     0     0     0
	      /dev/disk/by-id/wwn-0xb-part1   FAULTED      3     0     0  too many errors
	      /dev/disk/by-id/wwn-0xc-part1   ONLINE       0     0     0  (resilvering)
	    4523112310457124                  UNAVAIL      0     0     0  was /dev/disk/by-id/wwn-0xd-part1
	    spare-3                           ONLINE       0     0     0
	      /dev/disk/by-id/wwn-0xe-part1   REMOVED      0     0     0
	      /dev/disk/by-id/wwn-0xs-part1   ONLINE       0     0     0
	special	
	  mirror-2                            ONLINE       0     0     0
	    /dev/disk/by-id/wwn-0xf-part1     ONLINE       0     0     0
	    /dev/disk/by-id/wwn-0xg-part1     ONLINE       0     0     0
	logs	
	  mirror-4                            ONLINE       0     0     0
	    /dev/disk/by-id/wwn-0xh-part1     ONLINE       0     0     0
	    /dev/disk/by-id/wwn-0xi-part1     ONLINE       0     0     0
	cache
	  /dev/disk/by-id/wwn-0xj-part1       ONLINE       0     0     0
	spares
	  /dev/disk/by-id/wwn-0xs-part1       INUSE     currently in use
	  /dev/disk/by-id/wwn-0xt-part1       AVAIL   

errors: No known data errors

  pool: rpool
 state: ONLINE
config:

	NAME        STATE     READ WRITE CKSUM
	rpool       ONLINE       0     0     0
	  /dev/sda3 ONLINE       0     0     0

errors: No known data errors
`

const statusG = `  pool: tank
 state: DEGRADED
status: One or more devices could not be opened.
config:

	NAME                   STATE     READ WRITE CKSUM
	tank                   DEGRADED     0     0     0
	  100                  DEGRADED     0     0     0
	    101                ONLINE       0     0     0
	    102                DEGRADED     0     0     0
	      103              FAULTED      3     0     0  too many errors
	      104              ONLINE       0     0     0  (resilvering)
	    4523112310457124   UNAVAIL      0     0     0  was /dev/disk/by-id/wwn-0xd-part1
	    105                ONLINE       0     0     0
	      106              REMOVED      0     0     0
	      107              ONLINE       0     0     0
	special	
	  200                  ONLINE       0     0     0
	    201                ONLINE       0     0     0
	    202                ONLINE       0     0     0
	logs	
	  300                  ONLINE       0     0     0
	    301                ONLINE       0     0     0
	    302                ONLINE       0     0     0
	cache
	  400                  ONLINE       0     0     0
	spares
	  107                  INUSE     currently in use
	  500                  AVAIL   

errors: No known data errors

  pool: rpool
 state: ONLINE
config:

	NAME        STATE     READ WRITE CKSUM
	rpool       ONLINE       0     0     0
	  600       ONLINE       0     0     0

errors: No known data errors
`

const guidList = "rpool\tguid\t7000\t-\ntank\tguid\t9000\t-\n"

func TestParseZpoolStatus(t *testing.T) {
	pools, err := parseZpoolStatus(statusP)
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 2 || pools[0].name != "tank" || pools[1].name != "rpool" {
		t.Fatalf("pools = %+v, want tank and rpool", pools)
	}
	rows := pools[0].rows
	if len(rows) != 23 {
		t.Fatalf("tank has %d rows, want 23", len(rows))
	}
	checks := []struct {
		i    int
		want zpoolRow
	}{
		{0, zpoolRow{depth: 0, name: "tank", state: "DEGRADED"}},
		{1, zpoolRow{depth: 1, name: "raidz2-0", state: "DEGRADED"}},
		{4, zpoolRow{depth: 3, name: "/dev/disk/by-id/wwn-0xb-part1", state: "FAULTED", extra: "too many errors"}},
		{6, zpoolRow{depth: 2, name: "4523112310457124", state: "UNAVAIL", extra: "was /dev/disk/by-id/wwn-0xd-part1"}},
		{10, zpoolRow{depth: 0, name: "special"}},
		{14, zpoolRow{depth: 0, name: "logs"}},
		{21, zpoolRow{depth: 1, name: "/dev/disk/by-id/wwn-0xs-part1", state: "INUSE", extra: "currently in use"}},
		{22, zpoolRow{depth: 1, name: "/dev/disk/by-id/wwn-0xt-part1", state: "AVAIL"}},
	}
	for _, c := range checks {
		if diff := cmp.Diff(c.want, rows[c.i], cmp.AllowUnexported(zpoolRow{})); diff != "" {
			t.Errorf("row %d mismatch (-want +got):\n%s", c.i, diff)
		}
	}
}

func TestPoolMembers(t *testing.T) {
	pools, _ := parseZpoolStatus(statusP)
	gpools, _ := parseZpoolStatus(statusG)
	got, err := poolMembers(pools[0], gpools[0], "9000")
	if err != nil {
		t.Fatal(err)
	}
	want := []zpoolMember{
		{"/dev/disk/by-id/wwn-0xa-part1", "101", "ONLINE", "zfs > tank 9000 > raidz2 100 > disk 101"},
		{"/dev/disk/by-id/wwn-0xb-part1", "103", "FAULTED", "zfs > tank 9000 > raidz2 100 > replacing 102 > disk 103"},
		{"/dev/disk/by-id/wwn-0xc-part1", "104", "ONLINE", "zfs > tank 9000 > raidz2 100 > replacing 102 > disk 104"},
		{"/dev/disk/by-id/wwn-0xd-part1", "4523112310457124", "UNAVAIL", "zfs > tank 9000 > raidz2 100 > disk 4523112310457124"},
		{"/dev/disk/by-id/wwn-0xe-part1", "106", "REMOVED", "zfs > tank 9000 > raidz2 100 > spare 105 > disk 106"},
		// The in-use spare: the spares section is processed last and wins.
		{"/dev/disk/by-id/wwn-0xs-part1", "107", "INUSE", "zfs > spare tank > disk 107"},
		{"/dev/disk/by-id/wwn-0xf-part1", "201", "ONLINE", "zfs > tank 9000 > mirror 200 > disk 201"},
		{"/dev/disk/by-id/wwn-0xg-part1", "202", "ONLINE", "zfs > tank 9000 > mirror 200 > disk 202"},
		{"/dev/disk/by-id/wwn-0xh-part1", "301", "ONLINE", "zfs > tank 9000 > log > mirror 300 > disk 301"},
		{"/dev/disk/by-id/wwn-0xi-part1", "302", "ONLINE", "zfs > tank 9000 > log > mirror 300 > disk 302"},
		{"/dev/disk/by-id/wwn-0xj-part1", "400", "ONLINE", "zfs > l2arc tank > disk 400"},
		{"/dev/disk/by-id/wwn-0xt-part1", "500", "AVAIL", "zfs > spare tank > disk 500"},
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(zpoolMember{})); diff != "" {
		t.Errorf("poolMembers mismatch (-want +got):\n%s", diff)
	}

	got, err = poolMembers(pools[1], gpools[1], "7000")
	if err != nil {
		t.Fatal(err)
	}
	want = []zpoolMember{{"/dev/sda3", "600", "ONLINE", "zfs > rpool 7000 > disk 600"}}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(zpoolMember{})); diff != "" {
		t.Errorf("rpool members mismatch (-want +got):\n%s", diff)
	}
}

func TestPoolMembersMismatch(t *testing.T) {
	pools, _ := parseZpoolStatus(statusP)
	gpools, _ := parseZpoolStatus(statusG)
	short := gpools[0]
	short.rows = short.rows[:len(short.rows)-1]
	if _, err := poolMembers(pools[0], short, "9000"); err == nil {
		t.Error("row count mismatch: got nil error, want error")
	}
	shifted := gpools[0]
	shifted.rows = append([]zpoolRow(nil), shifted.rows...)
	shifted.rows[2].depth = 1
	if _, err := poolMembers(pools[0], shifted, "9000"); err == nil {
		t.Error("depth mismatch: got nil error, want error")
	}
}

func TestVdevTypeName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"raidz2-0", "raidz2"},
		{"mirror-17", "mirror"},
		{"spare-3", "spare"},
		{"replacing-0", "replacing"},
		{"draid2:8d:24c:2s-0", "draid2:8d:24c:2s"},
	}
	for _, tc := range tests {
		if got := vdevTypeName(tc.in); got != tc.want {
			t.Errorf("vdevTypeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAnnotateZFSExec runs the whole path through a fake Exec against an
// inventory holding some of the pool's disks, and checks uses, states, and
// the unmapped list.
func TestAnnotateZFSExec(t *testing.T) {
	c := &Collector{Exec: func(name string, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "status -P":
			return []byte(statusP), nil
		case "status -g":
			return []byte(statusG), nil
		case "get -Hp guid":
			return []byte(guidList), nil
		}
		t.Fatalf("unexpected command %s %v", name, args)
		return nil, nil
	}}
	inv := drivelist.NewInventory()
	a := &drivelist.Device{DeviceName: "sdb", Devices: []string{"/dev/sdb", "/dev/disk/by-id/wwn-0xa"}}
	s := &drivelist.Device{DeviceName: "sdc", Devices: []string{"/dev/sdc", "/dev/disk/by-id/wwn-0xs"}}
	root := &drivelist.Device{DeviceName: "sda", Devices: []string{"/dev/sda"}}
	for _, d := range []*drivelist.Device{a, s, root} {
		inv.Add(d)
	}
	if err := c.annotateZFSExec(inv); err != nil {
		t.Fatal(err)
	}
	if got, want := a.Uses, []string{"zfs > tank 9000 > raidz2 100 > disk 101"}; !cmp.Equal(got, want) {
		t.Errorf("sdb uses = %v, want %v", got, want)
	}
	if a.MemberState != "ONLINE" {
		t.Errorf("sdb state = %q, want ONLINE", a.MemberState)
	}
	if got, want := s.Uses, []string{"zfs > spare tank > disk 107"}; !cmp.Equal(got, want) {
		t.Errorf("sdc uses = %v, want %v", got, want)
	}
	if got, want := root.Uses, []string{"zfs > rpool 7000 > disk 600"}; !cmp.Equal(got, want) {
		t.Errorf("sda uses = %v, want %v", got, want)
	}
	if len(inv.Unmapped) != 10 {
		t.Errorf("Unmapped has %d members, want 10", len(inv.Unmapped))
	}
	for _, m := range inv.Unmapped {
		if m.Path == "/dev/disk/by-id/wwn-0xd-part1" {
			if m.State != "UNAVAIL" || m.GUID != "4523112310457124" || m.Pool != "tank" {
				t.Errorf("unmapped missing device = %+v", m)
			}
		}
	}
}

func TestAnnotateZFSExecNoZpool(t *testing.T) {
	c := Fixture(t.TempDir()) // no exec directory at all
	inv := drivelist.NewInventory()
	if err := c.annotateZFSExec(inv); err != nil {
		t.Errorf("no zpool: got error %v, want nil", err)
	}
}
