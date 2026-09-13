package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// harness drives a store with a fake clock. Reports are observed at the
// clock's current time, which advances between calls.
type harness struct {
	t   *testing.T
	s   *Store
	ctx context.Context
	now time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := &harness{t: t, s: s, ctx: context.Background(), now: time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)}
	s.now = func() time.Time { return h.now }
	return h
}

func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }

var (
	hostA = HostIdentity{MachineID: "aaaa", Hostname: "storage1", OS: "linux"}
	hostB = HostIdentity{MachineID: "bbbb", Hostname: "backup", OS: "linux"}
)

func dev(name, serial, wwn, expander, bay string, uses ...string) ReportDevice {
	return ReportDevice{
		DevName:   name,
		Identity:  DriveIdentity{WWN: wwn, Model: "HUH72808", Serial: serial},
		Bus:       "sas",
		SizeBytes: 8_000_000_000_000,
		Expander:  expander,
		Bay:       bay,
		Uses:      uses,
	}
}

var (
	devX = dev("sda", "X1", "0x5000000000000001", "expander-4:0", "1", "zfs > tank 1 > raidz2 10 > disk 100")
	devY = dev("sdb", "Y1", "0x5000000000000002", "expander-4:0", "2", "zfs > tank 1 > raidz2 10 > disk 101")
	devZ = dev("sdc", "Z1", "0x5000000000000003", "expander-4:0", "3")
)

// report submits a complete report for host with devices, observed now.
func (h *harness) report(host HostIdentity, devices ...ReportDevice) IngestResult {
	h.t.Helper()
	return h.submit(Report{Host: host, ObservedAt: h.now, Devices: devices, Complete: true})
}

func (h *harness) submit(r Report) IngestResult {
	h.t.Helper()
	res, err := h.s.Ingest(h.ctx, r)
	if err != nil {
		h.t.Fatalf("Ingest: %v", err)
	}
	return res
}

func (h *harness) mustAccept(res IngestResult) IngestResult {
	h.t.Helper()
	if !res.Accepted {
		h.t.Fatalf("report rejected: %s", res.RejectReason)
	}
	return res
}

// events returns the kinds of every event about the drive with serial,
// oldest first, with the detail of each.
func (h *harness) events(serial string) (kinds []string, details []map[string]any) {
	h.t.Helper()
	rows, err := h.s.db.Query(`SELECT e.kind, e.detail FROM event e JOIN drive d USING (drive_id) WHERE d.serial = ? ORDER BY e.event_id`, serial)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, detail string
		if err := rows.Scan(&kind, &detail); err != nil {
			h.t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal([]byte(detail), &m)
		kinds = append(kinds, kind)
		details = append(details, m)
	}
	return kinds, details
}

func (h *harness) hostEvents(hostname string) []string {
	h.t.Helper()
	rows, err := h.s.db.Query(`SELECT e.kind FROM event e JOIN host h USING (host_id) WHERE h.hostname = ? AND e.drive_id IS NULL ORDER BY e.event_id`, hostname)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var k string
		rows.Scan(&k)
		kinds = append(kinds, k)
	}
	return kinds
}

type placementFact struct {
	host, expander, bay, uses string
	firstSeen, lastSeen       int64
	ended                     bool
	endReason                 string
}

// placements returns every placement of the drive, oldest first.
func (h *harness) placements(serial string) []placementFact {
	h.t.Helper()
	rows, err := h.s.db.Query(`
		SELECT ho.hostname, p.enclosure, p.bay, p.uses, p.first_seen, p.last_seen, p.ended_at IS NOT NULL, p.end_reason
		FROM placement p JOIN drive d USING (drive_id) JOIN host ho USING (host_id)
		WHERE d.serial = ? ORDER BY p.placement_id`, serial)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []placementFact
	for rows.Next() {
		var p placementFact
		if err := rows.Scan(&p.host, &p.expander, &p.bay, &p.uses, &p.firstSeen, &p.lastSeen, &p.ended, &p.endReason); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func (h *harness) count(query string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.s.db.QueryRow(query, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func equalKinds(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestFirstReportAndHeartbeat(t *testing.T) {
	h := newHarness(t)
	res := h.mustAccept(h.report(hostA, devX, devY, devZ))
	if !res.Changed {
		t.Error("first report: Changed = false, want true")
	}
	if len(res.Statuses) != 3 || res.Statuses[0].Status != StatusOK {
		t.Errorf("statuses = %+v, want three ok", res.Statuses)
	}
	if got := h.hostEvents("storage1"); !equalKinds(got, []string{EventHostFirstSeen}) {
		t.Errorf("host events = %v", got)
	}
	for _, serial := range []string{"X1", "Y1", "Z1"} {
		if kinds, _ := h.events(serial); !equalKinds(kinds, []string{EventFirstSeen}) {
			t.Errorf("%s events = %v, want [first_seen]", serial, kinds)
		}
	}
	first := h.now.Unix()

	h.advance(5 * time.Minute)
	res = h.mustAccept(h.report(hostA, devX, devY, devZ))
	if res.Changed {
		t.Error("heartbeat: Changed = true, want false")
	}
	if n := h.count(`SELECT COUNT(*) FROM snapshot`); n != 1 {
		t.Errorf("snapshots after heartbeat = %d, want 1", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM event`); n != 4 {
		t.Errorf("events after heartbeat = %d, want 4", n)
	}
	ps := h.placements("Z1")
	if len(ps) != 1 || ps[0].firstSeen != first || ps[0].lastSeen != h.now.Unix() || ps[0].ended {
		t.Errorf("Z placement after heartbeat = %+v", ps)
	}
	var lastAt int64
	h.s.db.QueryRow(`SELECT last_at FROM snapshot`).Scan(&lastAt)
	if lastAt != h.now.Unix() {
		t.Errorf("snapshot.last_at = %d, want %d", lastAt, h.now.Unix())
	}
}

func TestVanishAndReappear(t *testing.T) {
	h := newHarness(t)
	h.report(hostA, devX, devY, devZ)
	h.advance(5 * time.Minute)
	h.report(hostA, devX, devY, devZ)
	lastConfirmed := h.now.Unix()

	h.advance(5 * time.Minute)
	res := h.mustAccept(h.report(hostA, devX, devY))
	if !res.Changed {
		t.Fatal("report without Z: Changed = false")
	}
	kinds, details := h.events("Z1")
	if !equalKinds(kinds, []string{EventFirstSeen, EventVanished}) {
		t.Fatalf("Z events = %v", kinds)
	}
	if got := details[1]["last_seen"]; got != float64(lastConfirmed) {
		t.Errorf("vanished.last_seen = %v, want %d", got, lastConfirmed)
	}
	ps := h.placements("Z1")
	if len(ps) != 1 || !ps[0].ended || ps[0].endReason != EndVanished || ps[0].lastSeen != lastConfirmed {
		t.Errorf("Z placement = %+v", ps)
	}
	// X and Y are untouched.
	if kinds, _ := h.events("X1"); !equalKinds(kinds, []string{EventFirstSeen}) {
		t.Errorf("X events = %v", kinds)
	}

	h.advance(6 * time.Hour)
	h.report(hostA, devX, devY, devZ)
	kinds, details = h.events("Z1")
	if !equalKinds(kinds, []string{EventFirstSeen, EventVanished, EventReappeared}) {
		t.Fatalf("Z events = %v", kinds)
	}
	re := details[2]
	if re["gap_secs"] != float64(h.now.Unix()-lastConfirmed) || re["same_slot"] != true || re["from_host"] != "storage1" {
		t.Errorf("reappeared detail = %v", re)
	}
	ps = h.placements("Z1")
	if len(ps) != 2 || ps[1].ended || ps[1].firstSeen != h.now.Unix() {
		t.Errorf("Z placements = %+v", ps)
	}
}

func TestMoveHostNewHostReportsFirst(t *testing.T) {
	h := newHarness(t)
	h.report(hostA, devX, devY)
	h.advance(time.Hour)
	h.report(hostB, dev("sdq", "X1", "0x5000000000000001", "expander-9:0", "7"))
	kinds, details := h.events("X1")
	if !equalKinds(kinds, []string{EventFirstSeen, EventMovedHost}) {
		t.Fatalf("X events = %v", kinds)
	}
	if details[1]["from_host"] != "storage1" || details[1]["to_bay"] != "7" || details[1]["from_bay"] != "1" {
		t.Errorf("moved_host detail = %v", details[1])
	}
	ps := h.placements("X1")
	if len(ps) != 2 || !ps[0].ended || ps[0].endReason != EndMoved || ps[1].host != "backup" || ps[1].ended {
		t.Errorf("X placements = %+v", ps)
	}
	// The old host noticing is not a second event.
	h.advance(time.Minute)
	h.report(hostA, devY)
	if kinds, _ := h.events("X1"); len(kinds) != 2 {
		t.Errorf("X events after old host reports = %v", kinds)
	}
}

func TestMoveHostOldHostReportsFirst(t *testing.T) {
	h := newHarness(t)
	h.report(hostA, devX, devY)
	h.advance(time.Hour)
	h.report(hostA, devY)
	h.advance(time.Hour)
	h.report(hostB, dev("sdq", "X1", "0x5000000000000001", "expander-9:0", "7", "spare"))
	kinds, details := h.events("X1")
	if !equalKinds(kinds, []string{EventFirstSeen, EventVanished, EventReappeared}) {
		t.Fatalf("X events = %v", kinds)
	}
	if details[2]["from_host"] != "storage1" || details[2]["same_slot"] != false {
		t.Errorf("reappeared detail = %v", details[2])
	}
}

func TestUseAndBayChanges(t *testing.T) {
	h := newHarness(t)
	h.report(hostA, devX, devZ)
	h.advance(time.Hour)
	z := devZ
	z.Uses = []string{"zfs > tank 1 > raidz2 10 > disk 102"}
	h.report(hostA, devX, z)
	kinds, details := h.events("Z1")
	if !equalKinds(kinds, []string{EventFirstSeen, EventUseChanged}) {
		t.Fatalf("Z events = %v", kinds)
	}
	if details[1]["from_uses"] == nil || details[1]["to_uses"] == nil {
		t.Errorf("use_changed detail = %v", details[1])
	}
	h.advance(time.Hour)
	z.Bay = "9"
	h.report(hostA, devX, z)
	kinds, _ = h.events("Z1")
	if !equalKinds(kinds, []string{EventFirstSeen, EventUseChanged, EventMovedBay}) {
		t.Fatalf("Z events = %v", kinds)
	}
	ps := h.placements("Z1")
	if len(ps) != 3 || ps[0].endReason != EndUseChanged || ps[1].endReason != EndMoved || ps[2].ended || ps[2].bay != "9" {
		t.Errorf("Z placements = %+v", ps)
	}
	// Use order does not matter.
	h.advance(time.Hour)
	x := devX
	x.Uses = []string{"mount > /data", devX.Uses[0]}
	h.report(hostA, x, z)
	x.Uses = []string{devX.Uses[0], "mount > /data"}
	h.advance(time.Hour)
	res := h.report(hostA, x, z)
	if res.Changed {
		t.Error("reordered uses counted as a change")
	}
}

func TestDegradedReports(t *testing.T) {
	h := newHarness(t)
	h.report(hostA, devX, devY, devZ)

	// Z's udevadm failed but it is still in bay 3: attributed, nothing vanishes.
	h.advance(time.Hour)
	broken := ReportDevice{DevName: "sdc", Expander: "expander-4:0", Bay: "3", Error: "udevadm: exit status 1"}
	res := h.mustAccept(h.report(hostA, devX, devY, broken))
	if !res.Changed {
		t.Fatal("degraded report: Changed = false")
	}
	if n := h.count(`SELECT COUNT(*) FROM event WHERE kind IN (?, ?)`, EventVanished, EventReportDegraded); n != 0 {
		t.Errorf("attributed device produced %d vanish/degraded events, want 0", n)
	}
	if ps := h.placements("Z1"); len(ps) != 1 || ps[0].ended || ps[0].lastSeen != h.now.Unix() {
		t.Errorf("Z placement after attribution = %+v", ps)
	}
	if n := h.count(`SELECT complete FROM snapshot ORDER BY snapshot_id DESC LIMIT 1`); n != 1 {
		t.Error("snapshot with an attributed device marked incomplete")
	}

	// A device that failed with no bay cannot be attributed: the report is
	// degraded, X is absent, and X must not vanish.
	h.advance(time.Hour)
	broken.Bay, broken.Expander = "", ""
	h.report(hostA, devY, broken)
	if got := h.hostEvents("storage1"); !equalKinds(got, []string{EventHostFirstSeen, EventReportDegraded}) {
		t.Errorf("host events = %v", got)
	}
	if kinds, _ := h.events("X1"); !equalKinds(kinds, []string{EventFirstSeen}) {
		t.Errorf("X events under degraded report = %v, want no vanish", kinds)
	}
	// Still degraded next time: no second report_degraded event.
	h.advance(time.Hour)
	broken.DevName = "sdd"
	h.report(hostA, devY, broken)
	if got := h.hostEvents("storage1"); len(got) != 2 {
		t.Errorf("host events repeated degraded = %v", got)
	}

	// A complete report finally says X and Z are gone.
	h.advance(time.Hour)
	h.report(hostA, devY)
	for _, serial := range []string{"X1", "Z1"} {
		if kinds, _ := h.events(serial); !equalKinds(kinds, []string{EventFirstSeen, EventVanished}) {
			t.Errorf("%s events after complete report = %v", serial, kinds)
		}
	}
}

func TestIncompleteReportChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.report(hostA, devX, devY)
	h.advance(time.Hour)
	x, y := devX, devY
	x.Uses, y.Uses = nil, nil // zpool failed: no uses at all
	h.submit(Report{Host: hostA, ObservedAt: h.now, Devices: []ReportDevice{x, y}, Complete: false, CollectorErrors: []string{"zpool status -P: exit status 1"}})
	if n := h.count(`SELECT COUNT(*) FROM event WHERE kind IN (?, ?)`, EventUseChanged, EventVanished); n != 0 {
		t.Errorf("incomplete report produced %d use/vanish events, want 0", n)
	}
	if ps := h.placements("X1"); len(ps) != 1 || ps[0].lastSeen != h.now.Unix() {
		t.Errorf("X placement under incomplete report = %+v", ps)
	}
	if got := h.hostEvents("storage1"); !equalKinds(got, []string{EventHostFirstSeen, EventReportDegraded}) {
		t.Errorf("host events = %v", got)
	}
}

func TestGhosts(t *testing.T) {
	h := newHarness(t)
	h.report(hostA, devX, devY, devZ)
	h.advance(time.Hour)
	member := PoolMember{Pool: "tank", Path: "/dev/disk/by-id/wwn-0x5000000000000003-part1", GUID: "999", State: "UNAVAIL"}
	h.submit(Report{Host: hostA, ObservedAt: h.now, Devices: []ReportDevice{devX, devY}, Unmapped: []PoolMember{member}, Complete: true})
	kinds, details := h.events("Z1")
	if !equalKinds(kinds, []string{EventFirstSeen, EventVanished, EventPoolMissingMember}) {
		t.Fatalf("Z events = %v", kinds)
	}
	if details[1]["still_in_pool"] != "tank" || details[1]["pool_state"] != "UNAVAIL" {
		t.Errorf("vanished detail lacks the pool reference: %v", details[1])
	}
	if n := h.count(`SELECT COUNT(*) FROM ghost g JOIN drive d USING (drive_id) WHERE d.serial = 'Z1' AND g.ended_at IS NULL`); n != 1 {
		t.Errorf("open ghosts for Z = %d, want 1", n)
	}
	h.advance(time.Hour)
	h.submit(Report{Host: hostA, ObservedAt: h.now, Devices: []ReportDevice{devX, devY}, Unmapped: []PoolMember{member}, Complete: true})
	if n := h.count(`SELECT COUNT(*) FROM ghost`); n != 1 {
		t.Errorf("ghost rows after repeat = %d, want 1", n)
	}
	h.advance(time.Hour)
	h.report(hostA, devX, devY)
	if n := h.count(`SELECT COUNT(*) FROM ghost WHERE ended_at IS NULL`); n != 0 {
		t.Errorf("open ghosts after the pool forgot the member = %d, want 0", n)
	}
}

func TestMemberStateChange(t *testing.T) {
	h := newHarness(t)
	x := devX
	x.MemberState = "ONLINE"
	h.report(hostA, x)
	h.advance(time.Hour)
	x.MemberState = "FAULTED"
	res := h.report(hostA, x)
	if !res.Changed {
		t.Fatal("state change not seen as a change")
	}
	kinds, details := h.events("X1")
	if !equalKinds(kinds, []string{EventFirstSeen, EventMemberStateChanged}) {
		t.Fatalf("X events = %v", kinds)
	}
	if details[1]["from"] != "ONLINE" || details[1]["to"] != "FAULTED" {
		t.Errorf("member_state_changed detail = %v", details[1])
	}
	if ps := h.placements("X1"); len(ps) != 1 {
		t.Errorf("state change split the placement: %+v", ps)
	}
}

func TestRejects(t *testing.T) {
	h := newHarness(t)
	res := h.submit(Report{Host: hostA, ObservedAt: h.now.Add(time.Hour), Devices: []ReportDevice{devX}, Complete: true})
	if res.Accepted || res.RejectReason != "clock_skew" {
		t.Errorf("future report: %+v", res)
	}
	h.report(hostA, devX)
	res = h.submit(Report{Host: hostA, ObservedAt: h.now.Add(-time.Minute), Devices: []ReportDevice{devX}, Complete: true})
	if res.Accepted || res.RejectReason != "stale" {
		t.Errorf("older report: %+v", res)
	}
	if n := h.count(`SELECT COUNT(*) FROM snapshot`); n != 1 {
		t.Errorf("rejected reports left %d snapshots, want 1", n)
	}
	// Same second as the last report: accepted, and a heartbeat.
	res = h.report(hostA, devX)
	if !res.Accepted || res.Changed {
		t.Errorf("same-second report: %+v, want accepted and unchanged", res)
	}
}

func TestIdentityConflictAndNoIdentity(t *testing.T) {
	h := newHarness(t)
	h.report(hostA, devX, devY)
	h.advance(time.Hour)
	// X's WWN with Y's serial: two keys, two drives.
	mixed := dev("sdz", "Y1", "0x5000000000000001", "expander-4:0", "5")
	nobody := ReportDevice{DevName: "sdu", Identity: DriveIdentity{Model: "USB thing"}, Bus: "usb"}
	h.report(hostA, mixed, nobody)
	if n := h.count(`SELECT COUNT(*) FROM event WHERE kind = ?`, EventIdentityConflict); n != 1 {
		t.Errorf("identity_conflict events = %d, want 1", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM drive`); n != 2 {
		t.Errorf("drives = %d, want 2 (no new drive under a conflict)", n)
	}
	// The unidentifiable USB device is recorded but does not degrade the
	// report or place anything.
	if n := h.count(`SELECT COUNT(*) FROM snapshot_device WHERE dev_name = 'sdu' AND drive_id IS NULL`); n != 1 {
		t.Error("no-identity device not recorded")
	}
	if got := h.hostEvents("storage1"); !equalKinds(got, []string{EventHostFirstSeen}) {
		t.Errorf("host events = %v, want no report_degraded", got)
	}
	h.advance(time.Hour)
	if n := h.count(`SELECT COUNT(*) FROM event WHERE kind = ?`, EventIdentityConflict); n != 1 {
		t.Errorf("conflict re-reported: %d events", n)
	}
}

func TestSweep(t *testing.T) {
	h := newHarness(t)
	h.report(hostA, devX)
	h.report(hostB, devY)
	h.advance(10 * time.Minute)
	h.report(hostB, devY)
	h.advance(10 * time.Minute) // A silent for 20 min: > 3 × 5 min
	n, err := h.s.Sweep(h.ctx, 5*time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v; want 1", n, err)
	}
	if got := h.hostEvents("storage1"); !equalKinds(got, []string{EventHostFirstSeen, EventHostStale}) {
		t.Errorf("A events = %v", got)
	}
	if got := h.hostEvents("backup"); !equalKinds(got, []string{EventHostFirstSeen}) {
		t.Errorf("B events = %v", got)
	}
	if ps := h.placements("X1"); ps[0].ended {
		t.Error("stale host's placement was closed")
	}
	n, _ = h.s.Sweep(h.ctx, 5*time.Minute)
	if n != 0 {
		t.Errorf("second Sweep marked %d hosts, want 0", n)
	}
	h.advance(time.Minute)
	h.report(hostA, devX)
	if got := h.hostEvents("storage1"); !equalKinds(got, []string{EventHostFirstSeen, EventHostStale, EventHostResumed}) {
		t.Errorf("A events after resuming = %v", got)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var first int
	s.db.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&first)
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s.Close()
	var second int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&second); err != nil || first == 0 || second != first {
		t.Errorf("schema_version rows = %d then %d, %v; want the same non-zero count", first, second, err)
	}
}

// TestExpanderRenamed: after a reboot the kernel numbers the SAS host
// differently, so every drive reports a new expander name in its old bay.
// That is a rename of the shelf, not eleven moves.
func TestExpanderRenamed(t *testing.T) {
	h := newHarness(t)
	a := dev("sda", "A1", "0x5000000000000011", "expander-6:0", "3", "zfs > backups 1 > raidz3 0 > disk 0")
	b := dev("sdb", "B1", "0x5000000000000012", "expander-6:0", "4", "zfs > backups 1 > raidz3 0 > disk 1")
	c := dev("sdc", "C1", "0x5000000000000013", "expander-6:0", "5", "zfs > backups 1 > raidz3 0 > disk 2")
	h.report(hostA, a, b, c)
	h.advance(time.Hour)
	// C is out for the reboot; A and B come back under expander-0:0.
	a.Expander, b.Expander = "expander-0:0", "expander-0:0"
	h.report(hostA, a, b)
	for _, serial := range []string{"A1", "B1"} {
		kinds, _ := h.events(serial)
		if len(kinds) != 1 || kinds[0] != EventFirstSeen {
			t.Errorf("%s events = %v, want only first_seen", serial, kinds)
		}
		ps := h.placements(serial)
		if len(ps) != 1 || ps[0].expander != "expander-0:0" || ps[0].ended {
			t.Errorf("%s placements = %+v", serial, ps)
		}
	}
	// C was absent from the report, but it sits in the same shelf.
	if ps := h.placements("C1"); len(ps) != 1 || ps[0].expander != "expander-0:0" || !ps[0].ended || ps[0].endReason != EndVanished {
		t.Errorf("C1 placements = %+v", ps)
	}
	if kinds := h.hostEvents("storage1"); len(kinds) != 2 || kinds[1] != EventEnclosureRenamed {
		t.Errorf("host events = %v", kinds)
	}
	var detail string
	h.s.db.QueryRow(`SELECT detail FROM event WHERE kind = ?`, EventEnclosureRenamed).Scan(&detail)
	for _, want := range []string{`"from":"expander-6:0"`, `"to":"expander-0:0"`, `"drives":2`} {
		if !strings.Contains(detail, want) {
			t.Errorf("rename detail %s lacks %s", detail, want)
		}
	}

	// The agent upgrades and starts sending SAS addresses: the key changes
	// under both drives again, same bays, so it is another rename.
	h.advance(time.Hour)
	a.ExpanderID, b.ExpanderID = "0x500605b000000001", "0x500605b000000001"
	h.report(hostA, a, b)
	if ps := h.placements("A1"); len(ps) != 1 || ps[0].expander != "0x500605b000000001" {
		t.Errorf("after SAS address: %+v", ps)
	}
	// A rename with the same address but a new kernel name is nothing at
	// all: the name is display only.
	h.advance(time.Hour)
	a.Expander, b.Expander = "expander-9:0", "expander-9:0"
	h.report(hostA, a, b)
	if kinds := h.hostEvents("storage1"); len(kinds) != 3 {
		t.Errorf("host events after dev rename = %v", kinds)
	}
	var dev string
	h.s.db.QueryRow(`SELECT enclosure_via FROM placement WHERE ended_at IS NULL LIMIT 1`).Scan(&dev)
	if dev != "expander-9:0" {
		t.Errorf("enclosure_via = %q, want expander-9:0", dev)
	}

	// One drive alone going to another expander's same bay is a move.
	h.advance(time.Hour)
	a.ExpanderID, a.Expander = "0x500605b000000002", "expander-9:1"
	h.report(hostA, a, b)
	if kinds, _ := h.events("A1"); len(kinds) != 2 || kinds[1] != EventMovedBay {
		t.Errorf("single drive: %v, want moved_bay", kinds)
	}
}

func TestEnclosureNames(t *testing.T) {
	h := fleet(t)
	all, err := h.s.ListEnclosures(h.ctx)
	if err != nil || len(all) == 0 {
		t.Fatalf("ListEnclosures = %+v, %v", all, err)
	}
	var shelf Enclosure // storage1's, where X sits
	for _, e := range all {
		if e.Hostname == "storage1" {
			shelf = e
		}
	}
	if shelf.Key == "" || shelf.Drives < 1 {
		t.Fatalf("storage1's enclosure missing from %+v", all)
	}
	e, err := h.s.NameEnclosure(h.ctx, shelf.Via, "front shelf", "the one by the door", "scott")
	if err != nil || e.Name != "front shelf" || e.Key != shelf.Key || e.Drives != shelf.Drives {
		t.Fatalf("NameEnclosure = %+v, %v", e, err)
	}
	d, _, _, err := h.s.GetDrive(h.ctx, "X1")
	if err != nil || d.Current == nil || d.Current.EnclosureName != "front shelf" || d.Current.EnclosureVia != shelf.Via {
		t.Errorf("placement after naming = %+v, %v", d.Current, err)
	}
	names, _ := h.s.EnclosureNames(h.ctx)
	if names[shelf.Key] != "front shelf" {
		t.Errorf("EnclosureNames = %v", names)
	}
	if _, err := h.s.NameEnclosure(h.ctx, "nosuch", "x", "", "scott"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown enclosure: %v", err)
	}
	// Two hosts whose kernels both call a shelf expander-7:0: the kernel
	// name is ambiguous, a distinctive part of either key is not.
	q := dev("sdz", "Q1", "0x5000000000000009", "expander-7:0", "1")
	q.EnclosureID, q.EnclosureVia, q.EnclosureViaID = "0x500605b0000000aa", "expander-7:0", "0x500605b0000000a0"
	h.report(hostB, dev("sdq", "Y1", "0x5000000000000002", "expander-9:0", "7", "spare"), q)
	r := dev("sdz", "R1", "0x500000000000000a", "expander-7:0", "1")
	r.EnclosureID, r.EnclosureVia, r.EnclosureViaID = "0x500605b0000000bb", "expander-7:0", "0x500605b0000000b0"
	h.report(HostIdentity{MachineID: "cccc", Hostname: "third", OS: "linux"}, r)
	var amb *AmbiguousError
	if _, err := h.s.NameEnclosure(h.ctx, "expander-7:0", "x", "", "scott"); !errors.As(err, &amb) || len(amb.Candidates) != 2 {
		t.Errorf("ambiguous kernel name: %v", err)
	}
	if e, err := h.s.NameEnclosure(h.ctx, "00aa", "left JBOD", "", "scott"); err != nil || e.Key != "0x500605b0000000aa" || e.Hostname != "backup" || e.Bays != 1 {
		t.Errorf("partial key: %+v, %v", e, err)
	}
	// A name resolves back to its enclosure.
	if key, err := h.s.ResolveEnclosure(h.ctx, "left JBOD"); err != nil || key != "0x500605b0000000aa" {
		t.Errorf("resolve by name: %q, %v", key, err)
	}
	if e, err := h.s.NameEnclosure(h.ctx, shelf.Key, "", "", "scott"); err != nil || e.Name != "" {
		t.Errorf("clear = %+v, %v", e, err)
	}
}

// TestEnclosureKeys: the SES enclosure id is the key; the fallbacks and
// the upgrade renames from older keys keep names and placements intact.
func TestEnclosureKeys(t *testing.T) {
	h := newHarness(t)
	// A 0.4 agent: drives keyed on the expander's address; the front-panel
	// drives on the HBA's own ports have no owner at all.
	a := dev("sda", "A1", "0x5000000000000011", "expander-11:0", "3", "zfs > space 1 > raidz2 0 > disk 0")
	b := dev("sdb", "B1", "0x5000000000000012", "expander-11:0", "4", "zfs > space 1 > raidz2 0 > disk 1")
	a.ExpanderID, b.ExpanderID = "0x5000ccab0200947e", "0x5000ccab0200947e"
	f := dev("sdq", "F1", "0x5000000000000021", "", "0", "zfs > space 1 > mirror 3 > disk 0")
	g := dev("sdr", "G1", "0x5000000000000022", "", "7", "zfs > space 1 > mirror 3 > disk 1")
	h.report(hostA, a, b, f, g)
	if _, err := h.s.NameEnclosure(h.ctx, "947e", "big shelf", "", "scott"); err != nil {
		t.Fatal(err)
	}
	if ps := h.placements("F1"); len(ps) != 1 || ps[0].expander != "" {
		t.Errorf("front panel drive before 0.7 = %+v", ps)
	}

	// A 0.7 agent: the shelf's enclosure id differs from its expander's
	// address, and the front panel has an enclosure of its own.
	h.advance(5 * time.Minute)
	for _, d := range []*ReportDevice{&a, &b} {
		d.EnclosureID, d.EnclosureVia, d.EnclosureViaID = "0x5000ccab020094ff", "expander-11:0", "0x5000ccab0200947e"
	}
	for _, d := range []*ReportDevice{&f, &g} {
		d.EnclosureID, d.EnclosureVia, d.EnclosureViaID = "0x500062b2047b51c0", "host11", "0x500062b2047b51c5"
	}
	h.report(hostA, a, b, f, g)
	for serial, key := range map[string]string{"A1": "0x5000ccab020094ff", "B1": "0x5000ccab020094ff", "F1": "0x500062b2047b51c0", "G1": "0x500062b2047b51c0"} {
		if kinds, _ := h.events(serial); len(kinds) != 1 {
			t.Errorf("%s events = %v, want only first_seen", serial, kinds)
		}
		if ps := h.placements(serial); len(ps) != 1 || ps[0].expander != key {
			t.Errorf("%s placements = %+v, want key %s", serial, ps, key)
		}
	}
	renames := h.hostEvents("storage1")
	n := 0
	for _, k := range renames {
		if k == EventEnclosureRenamed {
			n++
		}
	}
	if n != 2 {
		t.Errorf("host events = %v, want two renames", renames)
	}
	names, _ := h.s.EnclosureNames(h.ctx)
	if names["0x5000ccab020094ff"] != "big shelf" || names["0x5000ccab0200947e"] != "" {
		t.Errorf("name did not follow the rename: %v", names)
	}
	es, err := h.s.ListEnclosures(h.ctx)
	if err != nil || len(es) != 2 {
		t.Fatalf("ListEnclosures = %+v, %v", es, err)
	}
	if es[0].Via != "expander-11:0" || es[0].Name != "big shelf" || es[0].Drives != 2 || es[0].Bays != 2 || es[1].Via != "host11" || es[1].Drives != 2 {
		t.Errorf("enclosures = %+v", es)
	}
	d, _, _, err := h.s.GetDrive(h.ctx, "F1")
	if err != nil || d.Current == nil || d.Current.EnclosureVia != "host11" || d.Current.Bay != "0" {
		t.Errorf("F1 = %+v, %v", d.Current, err)
	}
}
