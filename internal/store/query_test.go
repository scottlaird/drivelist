package store

import (
	"errors"
	"testing"
	"time"
)

// fleet builds a small history: X, Y, Z on storage1; Z vanishes; Y moves to
// backup as a spare; Z is marked suspect; backup has a ghost.
func fleet(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.report(hostA, devX, devY, devZ)
	h.advance(time.Hour)
	h.report(hostA, devX, devY) // Z vanishes
	h.advance(time.Hour)
	h.report(hostB, dev("sdq", "Y1", "0x5000000000000002", "expander-9:0", "7", "zfs > spare tank > disk 55"))
	h.advance(time.Hour)
	h.submit(Report{Host: hostB, ObservedAt: h.now,
		Devices:  []ReportDevice{dev("sdq", "Y1", "0x5000000000000002", "expander-9:0", "7", "zfs > spare tank > disk 55")},
		Unmapped: []PoolMember{{Pool: "tank", Path: "/dev/disk/by-id/wwn-0x5000000000000009-part1", GUID: "77", State: "UNAVAIL"}},
		Complete: true})
	if _, err := h.s.Annotate(h.ctx, "Z1", StatusSuspect, "vanished twice this month", "scott@laptop"); err != nil {
		t.Fatalf("Annotate: %v", err)
	}
	return h
}

func TestListHosts(t *testing.T) {
	h := fleet(t)
	hosts, err := h.s.ListHosts(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 || hosts[0].Hostname != "backup" || hosts[1].Hostname != "storage1" {
		t.Fatalf("hosts = %+v", hosts)
	}
	backup, storage := hosts[0], hosts[1]
	if backup.DriveCount != 1 || backup.GhostCount != 1 || backup.MissingCount != 0 {
		t.Errorf("backup counts = %d drives, %d ghosts, %d missing", backup.DriveCount, backup.GhostCount, backup.MissingCount)
	}
	// Z vanished from storage1 and is suspect, which does not excuse absence.
	if storage.DriveCount != 1 || storage.MissingCount != 1 {
		t.Errorf("storage1 counts = %d drives, %d missing; want 1, 1", storage.DriveCount, storage.MissingCount)
	}
	if !storage.StaleSince.IsZero() || storage.LastReport.IsZero() {
		t.Errorf("storage1 times = %+v", storage)
	}
}

func TestResolveDrive(t *testing.T) {
	h := fleet(t)
	for ref, want := range map[string]string{
		"X1":                 "X1",
		"0x5000000000000002": "Y1",
		"5000000000000003":   "Z1",
		"0X5000000000000001": "X1",
		"Z":                  "Z1",
	} {
		id, err := h.s.ResolveDrive(h.ctx, ref)
		if err != nil {
			t.Errorf("ResolveDrive(%q): %v", ref, err)
			continue
		}
		ds, _ := h.s.drives(h.ctx, `WHERE d.drive_id = ?`, id)
		if ds[0].Serial != want {
			t.Errorf("ResolveDrive(%q) = %s, want %s", ref, ds[0].Serial, want)
		}
	}
	if _, err := h.s.ResolveDrive(h.ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown ref: err = %v, want ErrNotFound", err)
	}
	var amb *AmbiguousError
	if _, err := h.s.ResolveDrive(h.ctx, "0x50000000"); !errors.As(err, &amb) || len(amb.Candidates) != 3 {
		t.Errorf("prefix shared by three: err = %v", err)
	}
}

func TestListDrives(t *testing.T) {
	h := fleet(t)
	all, err := h.s.ListDrives(h.ctx, DriveFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("all drives = %d, want 3", len(all))
	}
	// Ordered by current host/expander/bay: backup bay 7 (Y), storage1 bay 1 (X), then unplaced Z.
	if all[0].Serial != "Y1" || all[1].Serial != "X1" || all[2].Serial != "Z1" {
		t.Errorf("order = %s %s %s", all[0].Serial, all[1].Serial, all[2].Serial)
	}
	if all[0].Current == nil || all[0].Current.Hostname != "backup" || all[0].Current.Bay != "7" {
		t.Errorf("Y current = %+v", all[0].Current)
	}
	if all[2].Current != nil || all[2].Last == nil || all[2].Last.EndReason != EndVanished || all[2].Status != StatusSuspect {
		t.Errorf("Z = %+v last=%+v", all[2], all[2].Last)
	}

	onA, err := h.s.ListDrives(h.ctx, DriveFilter{Host: "stor"})
	if err != nil || len(onA) != 1 || onA[0].Serial != "X1" {
		t.Errorf("drives on storage1 = %+v, %v", onA, err)
	}
	missing, err := h.s.ListDrives(h.ctx, DriveFilter{MissingOnly: true})
	if err != nil || len(missing) != 1 || missing[0].Serial != "Z1" {
		t.Errorf("missing = %+v, %v", missing, err)
	}
	sus, err := h.s.ListDrives(h.ctx, DriveFilter{Status: []string{StatusSuspect, StatusBad}})
	if err != nil || len(sus) != 1 || sus[0].Serial != "Z1" {
		t.Errorf("suspect/bad = %+v, %v", sus, err)
	}
	unused, err := h.s.ListDrives(h.ctx, DriveFilter{UnusedOnly: true})
	if err != nil || len(unused) != 0 {
		t.Errorf("unused = %+v, %v; want none (Z is not placed)", unused, err)
	}
	if _, err := h.s.ListDrives(h.ctx, DriveFilter{Host: "nosuch"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown host filter: %v", err)
	}
}

func TestGetDriveAndHistory(t *testing.T) {
	h := fleet(t)
	d, keys, last, err := h.s.GetDrive(h.ctx, "Z1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusSuspect || last == nil || last.Kind != EventStatusChanged || last.Source != "user:scott@laptop" {
		t.Errorf("Z = %+v, last status event = %+v", d, last)
	}
	if len(keys) != 2 || keys[0] != "serial:HUH72808/Z1" || keys[1] != "wwn:0x5000000000000003" {
		t.Errorf("keys = %v", keys)
	}

	_, ps, evs, err := h.s.DriveHistory(h.ctx, "Y1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].Hostname != "storage1" || ps[0].EndReason != EndMoved || ps[1].Hostname != "backup" || !ps[1].EndedAt.IsZero() {
		t.Errorf("Y placements = %+v", ps)
	}
	if len(evs) != 2 || evs[0].Kind != EventFirstSeen || evs[1].Kind != EventMovedHost || evs[1].Hostname != "backup" {
		t.Errorf("Y events = %+v", evs)
	}
	if ps[1].Uses[0] != "zfs > spare tank > disk 55" {
		t.Errorf("Y current uses = %v", ps[1].Uses)
	}
}

func TestListEvents(t *testing.T) {
	h := fleet(t)
	evs, err := h.s.ListEvents(h.ctx, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 || evs[0].Kind != EventStatusChanged {
		t.Errorf("newest event = %+v, want the status change", evs[0])
	}
	vanished, err := h.s.ListEvents(h.ctx, EventFilter{Kinds: []string{EventVanished}})
	if err != nil || len(vanished) != 1 || vanished[0].Serial != "Z1" || vanished[0].Hostname != "storage1" {
		t.Errorf("vanished events = %+v, %v", vanished, err)
	}
	onB, err := h.s.ListEvents(h.ctx, EventFilter{Host: "backup"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range onB {
		if e.Hostname != "backup" {
			t.Errorf("event on wrong host: %+v", e)
		}
	}
	recent, _ := h.s.ListEvents(h.ctx, EventFilter{Since: h.now.Add(-30 * time.Minute)})
	if len(recent) != 2 { // pool_missing_member and the status change
		t.Errorf("events in the last 30 min = %d, want 2: %+v", len(recent), recent)
	}
	one, _ := h.s.ListEvents(h.ctx, EventFilter{Limit: 1})
	if len(one) != 1 {
		t.Errorf("Limit 1 returned %d", len(one))
	}
}

func TestListMissing(t *testing.T) {
	h := fleet(t)
	drives, ghosts, err := h.s.ListMissing(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(drives) != 1 || drives[0].Serial != "Z1" || drives[0].Last == nil || drives[0].Last.Bay != "3" {
		t.Errorf("missing drives = %+v", drives)
	}
	if len(ghosts) != 1 || ghosts[0].Hostname != "backup" || ghosts[0].Pool != "tank" || ghosts[0].State != "UNAVAIL" || ghosts[0].Serial != "" {
		t.Errorf("ghosts = %+v", ghosts)
	}
	// Shelving Z excuses its absence.
	if _, err := h.s.Annotate(h.ctx, "Z1", StatusShelved, "on the shelf", "scott@laptop"); err != nil {
		t.Fatal(err)
	}
	drives, _, _ = h.s.ListMissing(h.ctx)
	if len(drives) != 0 {
		t.Errorf("shelved drive still listed as missing: %+v", drives)
	}
}

func TestAnnotateValidation(t *testing.T) {
	h := fleet(t)
	if _, err := h.s.Annotate(h.ctx, "X1", "broken", "", "me"); err == nil {
		t.Error("bad status accepted")
	}
	if _, err := h.s.Annotate(h.ctx, "X1", "", "", "me"); err == nil {
		t.Error("empty annotation accepted")
	}
	ev, err := h.s.Annotate(h.ctx, "X1", "", "RMA 4471 opened", "me")
	if err != nil || ev.Kind != EventNote || ev.Serial != "X1" {
		t.Errorf("note = %+v, %v", ev, err)
	}
	d, _, _, _ := h.s.GetDrive(h.ctx, "X1")
	if d.Status != StatusOK {
		t.Errorf("a note changed the status to %s", d.Status)
	}
}
