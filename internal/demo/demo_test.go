package demo

import (
	"testing"

	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
)

func TestKey(t *testing.T) {
	for _, tc := range []struct {
		body map[string]any
		want string
	}{
		{nil, "index"},
		{map[string]any{"problems": false}, "index"},
		{map[string]any{"problems": true}, "problems=true"},
		{map[string]any{"since": true}, "index"},
		{map[string]any{"limit": 500, "kinds": []string{}}, "limit=500"},
		{map[string]any{"limit": 500, "kinds": []string{"moved_bay"}}, "kinds=moved_bay,limit=500"},
		{map[string]any{"host": "nas", "limit": 100}, "host=nas,limit=100"},
		{map[string]any{"ref": "dmi:KCS0GX0000TB"}, "ref=dmi%3AKCS0GX0000TB"},
		{map[string]any{"ref": "a b/c"}, "ref=a%20b%2Fc"},
	} {
		if got := Key(tc.body); got != tc.want {
			t.Errorf("Key(%v) = %q, want %q", tc.body, got, tc.want)
		}
	}
}

func TestSanitizer(t *testing.T) {
	names := Names{
		Hosts:   map[string]string{"fs2": "nas", "pbs1": "backups", "scottstudio": "mac1"},
		Replace: map[string]string{".internal.example.org": ""},
		Clear:   []string{"machine_id"},
	}
	hosts := []string{"fs2", "d1", "mgmt1", "pbs1", "scottstudio.internal.example.org", "d2"}
	s := NewSanitizer(names, hosts, map[string]string{"d2": "server7"})

	for real, want := range map[string]string{"fs2": "nas", "pbs1": "backups", "d2": "server7", "d1": "server1", "mgmt1": "server2", "scottstudio.internal.example.org": "server3"} {
		if got := s.Mapping()[real]; got != want {
			t.Errorf("mapping[%q] = %q, want %q", real, got, want)
		}
	}
	for in, want := range map[string]string{
		"fs2":                              "nas",
		"fs2-front":                        "nas-front",
		"fs20":                             "fs20",
		`{"from_host":"pbs1","to":"d1"}`:   `{"from_host":"backups","to":"server1"}`,
		"scottstudio.internal.example.org": "server3",
		"host scottstudio":                 "host mac1",
		"sd 6:0:12:0: [sdm] tag#0 FAILED":  "sd 6:0:12:0: [sdm] tag#0 FAILED",
	} {
		if got := s.Text(in); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}

	// Groups: switches are numbered on their own. A switch the last export
	// numbered as a server is renumbered; everything else keeps its name.
	grouped := names
	grouped.Groups = []Group{{Match: "sw-*", Names: "switch%d"}}
	g := NewSanitizer(grouped, append(hosts, "sw-garage-sn3800", "sw-desk100", "sw-garage-sn2410"),
		map[string]string{"d2": "server7", "d1": "server1", "sw-desk100": "server15", "sw-garage-sn3800": "switch2"})
	for real, want := range map[string]string{"d1": "server1", "d2": "server7", "mgmt1": "server2", "sw-garage-sn3800": "switch2", "sw-desk100": "switch1", "sw-garage-sn2410": "switch3", "fs2": "nas"} {
		if got := g.Mapping()[real]; got != want {
			t.Errorf("grouped mapping[%q] = %q, want %q", real, got, want)
		}
	}
	if got := g.Text("sw-desk100 enp22"); got != "switch1 enp22" {
		t.Errorf("grouped Text = %q", got)
	}

	msg := &pb.ListHostsResponse{Hosts: []*pb.Host{{Hostname: "fs2", MachineId: "abc"}, {Hostname: "d1", MachineId: "def"}}}
	s.Message(msg)
	if msg.Hosts[0].Hostname != "nas" || msg.Hosts[0].MachineId != "" || msg.Hosts[1].Hostname != "server1" {
		t.Errorf("Message() = %v", msg)
	}
	ev := &pb.ListEventsResponse{Events: []*pb.Event{{Hostname: "pbs1", Detail: `{"from_host":"fs2"}`}}}
	s.Message(ev)
	if ev.Events[0].Hostname != "backups" || ev.Events[0].Detail != `{"from_host":"nas"}` {
		t.Errorf("Message(events) = %v", ev.Events[0])
	}
}
