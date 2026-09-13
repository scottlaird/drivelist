package store

import (
	"testing"
	"time"
)

func TestMergeDrives(t *testing.T) {
	h := newHarness(t)
	// The same physical drive seen first over USB with only a serial, then
	// on the SAS backplane with only a WWN: two records.
	usb := ReportDevice{DevName: "sdu", Identity: DriveIdentity{Model: "HUH72808", Serial: "X1"}, Bus: "usb"}
	h.report(hostA, usb)
	h.advance(time.Hour)
	sas := ReportDevice{DevName: "sda", Identity: DriveIdentity{WWN: "0x5000000000000001"}, Bus: "sas", Expander: "expander-4:0", Bay: "1"}
	h.report(hostA, sas) // usb vanishes, sas appears: two drives, two histories
	if n := h.count(`SELECT COUNT(*) FROM drive`); n != 2 {
		t.Fatalf("drives before merge = %d, want 2", n)
	}
	h.advance(time.Hour)
	ev, err := h.s.MergeDrives(h.ctx, "0x5000000000000001", "X1", "scott@laptop")
	if err != nil {
		t.Fatalf("MergeDrives: %v", err)
	}
	if ev.Kind != EventMerged || ev.WWN != "0x5000000000000001" || ev.Source != "user:scott@laptop" {
		t.Errorf("merged event = %+v", ev)
	}
	// One drive with both keys; the old serial resolves to it.
	into, err := h.s.ResolveDrive(h.ctx, "X1")
	if err != nil {
		t.Fatal(err)
	}
	d, keys, _, err := h.s.GetDrive(h.ctx, "0x5000000000000001")
	if err != nil || d.ID != into || len(keys) != 2 || d.Serial != "X1" || d.WWN != "0x5000000000000001" {
		t.Errorf("merged drive = %+v keys=%v err=%v", d, keys, err)
	}
	if ds, _ := h.s.ListDrives(h.ctx, DriveFilter{}); len(ds) != 1 {
		t.Errorf("ListDrives after merge = %d, want 1", len(ds))
	}
	// History is joined: first_seen (usb), vanished, first_seen (sas), merged.
	_, ps, evs, err := h.s.DriveHistory(h.ctx, "X1")
	if err != nil || len(ps) != 2 || !ps[0].EndedAt.IsZero() == false && ps[0].EndReason != EndVanished || ps[1].EndedAt.IsZero() == false {
		t.Errorf("history placements = %+v, %v", ps, err)
	}
	kinds := []string{}
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	// The second report's first_seen (sas) and vanished (usb) share a
	// timestamp; the placement pass runs before the vanish pass.
	if !equalKinds(kinds, []string{EventFirstSeen, EventFirstSeen, EventVanished, EventMerged}) {
		t.Errorf("history events = %v", kinds)
	}
	// A later report naming both identity halves lands on the one drive.
	h.advance(time.Hour)
	both := ReportDevice{DevName: "sda", Identity: DriveIdentity{WWN: "0x5000000000000001", Model: "HUH72808", Serial: "X1"}, Bus: "sas", Expander: "expander-4:0", Bay: "1"}
	res := h.report(hostA, both)
	if res.Changed {
		t.Error("report with both keys was seen as a change")
	}
	if n := h.count(`SELECT COUNT(*) FROM drive`); n != 2 {
		t.Errorf("drive rows = %d, want 2 (target plus the merged record)", n)
	}
	if _, err := h.s.MergeDrives(h.ctx, "X1", "0x5000000000000001", "me"); err == nil {
		t.Error("merging a drive into itself succeeded")
	}
}

func TestMergeDrivesClosesDuplicateOpenPlacement(t *testing.T) {
	h := newHarness(t)
	h.report(hostA, dev("sda", "X1", "", "expander-4:0", "1"))
	h.advance(time.Minute)
	h.report(hostB, ReportDevice{DevName: "sdq", Identity: DriveIdentity{WWN: "0x5000000000000001"}, Expander: "expander-9:0", Bay: "7"})
	h.advance(time.Minute)
	if _, err := h.s.MergeDrives(h.ctx, "0x5000000000000001", "X1", "me"); err != nil {
		t.Fatal(err)
	}
	ps := h.placements("X1")
	open := 0
	for _, p := range ps {
		if !p.ended {
			open++
			if p.host != "backup" {
				t.Errorf("the older placement stayed open: %+v", p)
			}
		} else if p.endReason != EndMerged {
			t.Errorf("closed placement reason = %q, want merged", p.endReason)
		}
	}
	if open != 1 {
		t.Errorf("open placements after merge = %d, want 1", open)
	}
}

func TestMergeHosts(t *testing.T) {
	h := newHarness(t)
	h.report(hostA, devX, devY)
	h.advance(time.Hour)
	// Reinstalled: same hostname, new machine id, same drives.
	reinstalled := HostIdentity{MachineID: "aaaa-new", Hostname: "storage1", OS: "linux"}
	h.report(reinstalled, devX, devY)
	hosts, _ := h.s.ListHosts(h.ctx)
	if len(hosts) != 2 {
		t.Fatalf("hosts before merge = %d, want 2", len(hosts))
	}
	// ResolveHost by name is ambiguous now; merge by machine id via the
	// ids the listing exposes.
	if _, err := h.s.ResolveHost(h.ctx, "storage1"); err == nil {
		t.Log("note: two hosts share the name; resolution picked the exact match")
	}
	var oldID, newID int64
	for _, x := range hosts {
		if x.MachineID == "aaaa" {
			oldID = x.ID
		} else {
			newID = x.ID
		}
	}
	if oldID == 0 || newID == 0 {
		t.Fatal("host ids")
	}
	h.advance(time.Minute)
	ev, err := h.s.MergeHosts(h.ctx, "aaaa-new", "aaaa", "scott@laptop")
	if err != nil {
		t.Fatalf("MergeHosts: %v", err)
	}
	if ev.Kind != EventHostMerged || ev.Hostname != "storage1" {
		t.Errorf("host_merged event = %+v", ev)
	}
	hosts, _ = h.s.ListHosts(h.ctx)
	if len(hosts) != 1 || hosts[0].ID != newID || hosts[0].DriveCount != 2 {
		t.Errorf("hosts after merge = %+v", hosts)
	}
	// Each drive is open once, on the merged host; the older duplicates
	// closed as merged.
	for _, serial := range []string{"X1", "Y1"} {
		open := 0
		for _, p := range h.placements(serial) {
			if !p.ended {
				open++
			} else if p.endReason != EndMerged && p.endReason != EndMoved {
				// The reinstalled host's first report looked like a move
				// from the old host; that closed the old placement before
				// the merge and is left as history.
				t.Errorf("%s: closed with %q", serial, p.endReason)
			}
		}
		if open != 1 {
			t.Errorf("%s open placements = %d, want 1", serial, open)
		}
	}
	// A report still arriving under the old machine id lands on the merged host.
	h.advance(time.Minute)
	res := h.report(hostA, devX, devY)
	if !res.Accepted || res.Changed {
		t.Errorf("report under the old machine id: %+v", res)
	}
	hosts, _ = h.s.ListHosts(h.ctx)
	if len(hosts) != 1 {
		t.Errorf("old machine id created a host again: %+v", hosts)
	}
}
