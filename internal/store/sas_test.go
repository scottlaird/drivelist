package store

import (
	"strings"
	"testing"
	"time"
)

// sasReport is a two-node topology: an HBA with a 4-wide port to an
// expander carrying two drives, X on phy 4 at 12G and Y on phy 5 at 6G.
func sasReport(host HostIdentity, devices ...ReportDevice) Report {
	const hba, exp = "0x500605b00a1b2c00", "0x500605b00a1b2c3d"
	r := Report{Host: host, Devices: devices, Complete: true}
	r.SASNodes = []SASNode{
		{Kind: "hba", Name: "host4", Address: hba, Vendor: "mpt3sas", Product: "SAS9300-8i", Revision: "16.00.12.00"},
		{Kind: "expander", Name: "expander-4:0", Address: exp, Vendor: "LSI", Product: "SAS2X36", Revision: "0e0b", ParentAddress: hba, UpstreamPort: "port-4:0"},
	}
	for i := 0; i < 4; i++ {
		r.SASPhys = append(r.SASPhys, SASPhy{OwnerAddress: hba, PhyID: i, Name: "phy-4:" + string(rune('0'+i)), Port: "port-4:0", PortWidth: 4, Rate: "12.0 Gbit", RateGbit: 12, AttachedKind: "expander", Attached: "expander-4:0", AttachedAddress: exp, Enabled: true})
		r.SASPhys = append(r.SASPhys, SASPhy{OwnerAddress: exp, PhyID: i, Name: "phy-4:0:" + string(rune('0'+i)), Rate: "12.0 Gbit", RateGbit: 12, AttachedKind: "upstream", Enabled: true})
	}
	r.SASPhys = append(r.SASPhys,
		SASPhy{OwnerAddress: exp, PhyID: 4, Name: "phy-4:0:4", Port: "port-4:0:0", PortWidth: 1, Rate: "12.0 Gbit", RateGbit: 12, AttachedKind: "drive", Attached: "end_device-4:0:0", AttachedAddress: "0x5000000000000001", DevName: "sda", Bay: "1", Enabled: true},
		SASPhy{OwnerAddress: exp, PhyID: 5, Name: "phy-4:0:5", Port: "port-4:0:1", PortWidth: 1, Rate: "6.0 Gbit", RateGbit: 6, AttachedKind: "drive", Attached: "end_device-4:0:1", AttachedAddress: "0x5000000000000002", DevName: "sdb", Bay: "2", Enabled: true},
	)
	return r
}

func (h *harness) sasEvents(kind string) []string {
	h.t.Helper()
	rows, err := h.s.db.Query(`SELECT detail FROM event WHERE kind = ? ORDER BY event_id`, kind)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		rows.Scan(&d)
		out = append(out, d)
	}
	return out
}

func TestIngestSAS(t *testing.T) {
	h := newHarness(t)
	r := sasReport(hostA, devX, devY)
	r.ObservedAt = h.now
	h.submit(r)
	if n := h.count(`SELECT COUNT(*) FROM sas_node WHERE gone_at IS NULL`); n != 2 {
		t.Errorf("nodes = %d, want 2", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM sas_phy WHERE gone_at IS NULL`); n != 10 {
		t.Errorf("phys = %d, want 10", n)
	}
	if got := h.sasEvents(EventSASNodeChanged); len(got) != 2 || !strings.Contains(got[1], `"change":"appeared"`) {
		t.Errorf("node events on first sight = %v", got)
	}
	// The drive behind phy 4 is X.
	if n := h.count(`SELECT COUNT(*) FROM sas_phy p JOIN drive d USING (drive_id) WHERE p.phy_id = 4 AND d.serial = 'X1'`); n != 1 {
		t.Error("phy 4 not attributed to X1")
	}

	// A heartbeat with nothing changed: no events, no samples.
	h.advance(5 * time.Minute)
	r.ObservedAt = h.now
	h.submit(r)
	if n := h.count(`SELECT COUNT(*) FROM event WHERE kind LIKE 'sas_%'`); n != 2 {
		t.Errorf("events after heartbeat = %d, want 2", n)
	}

	// Y's link drops to 3G and its phy counts errors; an upstream phy
	// counts errors too; the HBA port loses a lane.
	h.advance(5 * time.Minute)
	r2 := sasReport(hostA, devX, devY)
	r2.ObservedAt = h.now
	r2.SASPhys = r2.SASPhys[:0:0]
	for _, p := range sasReport(hostA, devX, devY).SASPhys {
		switch {
		case p.OwnerAddress == r2.SASNodes[0].Address && p.PhyID == 3:
			p.Port, p.PortWidth, p.Rate, p.RateGbit, p.AttachedKind, p.Attached, p.AttachedAddress = "", 0, "Unknown", 0, "", "", ""
		case p.OwnerAddress == r2.SASNodes[0].Address:
			p.PortWidth = 3
		case p.PhyID == 5:
			p.Rate, p.RateGbit, p.LossDwordSync, p.InvalidDword = "3.0 Gbit", 3, 7, 2
		case p.AttachedKind == "upstream" && p.PhyID == 0:
			p.InvalidDword = 12
		}
		r2.SASPhys = append(r2.SASPhys, p)
	}
	h.submit(r2)
	links := h.sasEvents(EventSASLinkChanged)
	if len(links) != 2 { // phy 5 (12->3... no: 6->3) and HBA phy 3 (12 -> Unknown)
		t.Fatalf("link events = %v", links)
	}
	var yLinks int
	for _, l := range links {
		if strings.Contains(l, `"from":"6.0 Gbit"`) && strings.Contains(l, `"to":"3.0 Gbit"`) && strings.Contains(l, `"dev_name":"sdb"`) {
			yLinks++
		}
	}
	if yLinks != 1 {
		t.Errorf("Y's link event missing from %v", links)
	}
	if kinds, _ := h.events("Y1"); kinds[len(kinds)-2] != EventSASLinkChanged || kinds[len(kinds)-1] != EventSASErrors {
		t.Errorf("Y1 events = %v, want sas_link_changed then sas_errors last", kinds)
	}
	if errs := h.sasEvents(EventSASErrors); len(errs) != 2 {
		t.Errorf("sas_errors events = %v, want one for Y's phy and one for the upstream phy", errs)
	} else if !strings.Contains(errs[0]+errs[1], `"grew":{"disparity_error":0,"invalid_dword":2,"loss_dword_sync":7,"phy_reset_problem":0}`) {
		t.Errorf("growth detail wrong in %v", errs)
	}
	if ports := h.sasEvents(EventSASPortChanged); len(ports) != 1 || !strings.Contains(ports[0], `"from":4,`) || !strings.Contains(ports[0], `"to":3`) {
		t.Errorf("port events = %v", ports)
	}
	if n := h.count(`SELECT COUNT(*) FROM sas_phy_sample`); n != 2 {
		t.Errorf("samples = %d, want 2", n)
	}

	// Same day, counters grow again: a sample, but no second warning.
	h.advance(5 * time.Minute)
	r3 := r2
	r3.ObservedAt = h.now
	r3.SASPhys = append([]SASPhy(nil), r2.SASPhys...)
	for i := range r3.SASPhys {
		if r3.SASPhys[i].PhyID == 5 && r3.SASPhys[i].AttachedKind == "drive" {
			r3.SASPhys[i].LossDwordSync = 9
		}
	}
	h.submit(r3)
	if n := h.count(`SELECT COUNT(*) FROM sas_phy_sample`); n != 3 {
		t.Errorf("samples after growth = %d, want 3", n)
	}
	if errs := h.sasEvents(EventSASErrors); len(errs) != 2 {
		t.Errorf("a second sas_errors on the same day: %v", errs)
	}
	// Most growth first: the upstream phy's 12, then Y's 11 over two reports.
	rows, err := h.s.SASErrors(h.ctx, "", h.now.Add(-time.Hour))
	if err != nil || len(rows) != 2 || rows[0].AttachedKind != "upstream" || rows[0].InvalidDword != 12 ||
		rows[1].LossDwordSync != 9 || rows[1].InvalidDword != 2 || rows[1].Serial != "Y1" || rows[1].Samples != 2 {
		t.Errorf("SASErrors = %+v, %v", rows, err)
	}

	// Reboot: counters back below what was seen. No sample, no event.
	h.advance(time.Hour)
	r4 := sasReport(hostA, devX, devY)
	r4.ObservedAt = h.now
	h.submit(r4)
	if n := h.count(`SELECT COUNT(*) FROM sas_phy_sample`); n != 3 {
		t.Errorf("samples after reboot = %d, want 3", n)
	}
	// The link came back to 6G and the port to 4 wide: events say so.
	if links := h.sasEvents(EventSASLinkChanged); len(links) != 4 {
		t.Errorf("link events after reboot = %d, want 4", len(links))
	}

	// The expander vanishes.
	h.advance(5 * time.Minute)
	r5 := sasReport(hostA)
	r5.ObservedAt = h.now
	r5.SASNodes = r5.SASNodes[:1]
	var hbaOnly []SASPhy
	for _, p := range r5.SASPhys {
		if p.OwnerAddress == r5.SASNodes[0].Address {
			p.Port, p.PortWidth, p.Rate, p.RateGbit, p.AttachedKind, p.Attached, p.AttachedAddress = "", 0, "Unknown", 0, "", "", ""
			hbaOnly = append(hbaOnly, p)
		}
	}
	r5.SASPhys = hbaOnly
	h.submit(r5)
	if got := h.sasEvents(EventSASNodeChanged); len(got) != 3 || !strings.Contains(got[2], `"change":"vanished"`) || !strings.Contains(got[2], `"name":"expander-4:0"`) {
		t.Errorf("node events = %v", got)
	}
	nodes, phys, err := h.s.SASState(h.ctx, "storage1")
	if err != nil || len(nodes) != 2 || nodes[1].GoneAt.IsZero() || nodes[0].GoneAt.IsZero() == false {
		t.Errorf("SASState nodes = %+v, %v", nodes, err)
	}
	gone := 0
	for _, p := range phys {
		if !p.GoneAt.IsZero() {
			gone++
		}
	}
	if gone != 6 {
		t.Errorf("gone phys = %d, want 6 (the expander's)", gone)
	}

	// A report with no topology at all changes nothing (older agent).
	h.advance(5 * time.Minute)
	h.report(hostA, devX, devY)
	if got := h.sasEvents(EventSASNodeChanged); len(got) != 3 {
		t.Errorf("a topology-less report changed nodes: %v", got)
	}
	totals, err := h.s.SASPhyTotals(h.ctx)
	if err != nil || len(totals) != 0 {
		t.Errorf("SASPhyTotals with everything down = %+v, %v", totals, err)
	}
}
