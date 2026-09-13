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

// TestEnclosuresFromEmptyBays: an enclosure with nothing in it (a rear
// backplane) is known only from the empty bays the agent reports, with
// its model from the SAS node that reaches it, and can be named.
func TestEnclosuresFromEmptyBays(t *testing.T) {
	h := newHarness(t)
	x, y := devX, devY
	for _, d := range []*ReportDevice{&x, &y} {
		d.ExpanderID, d.EnclosureID, d.EnclosureVia, d.EnclosureViaID = "0x500605b00a1b2c3d", "0x500605b00a1b2c3e", "expander-4:0", "0x500605b00a1b2c3d"
	}
	r := sasReport(hostA, x, y)
	r.ObservedAt = h.now
	r.SASNodes = append(r.SASNodes, SASNode{Kind: "expander", Name: "expander-4:1", Address: "0x500304801ea12dff", Vendor: "LSI", Product: "SAS3x28", Revision: "0601", ParentAddress: "0x500605b00a1b2c3d", UpstreamPort: "port-4:0:9"})
	r.EmptyBays = []ReportBay{
		{EnclosureID: "0x500605b00a1b2c3e", EnclosureVia: "expander-4:0", EnclosureViaID: "0x500605b00a1b2c3d", Bay: "3"},
		{EnclosureID: "0x500304801ea12df0", EnclosureVia: "expander-4:1", EnclosureViaID: "0x500304801ea12dff", Bay: "0"},
	}
	h.submit(r)
	es, err := h.s.ListEnclosures(h.ctx)
	if err != nil || len(es) != 2 {
		t.Fatalf("ListEnclosures = %+v, %v", es, err)
	}
	if es[0].Via != "expander-4:0" || es[0].Drives != 2 || es[0].Bays != 3 || es[0].Product != "LSI SAS2X36" || es[0].Key != "0x500605b00a1b2c3e" ||
		es[1].Via != "expander-4:1" || es[1].Drives != 0 || es[1].Bays != 1 || es[1].Product != "LSI SAS3x28" || es[1].Key != "0x500304801ea12df0" {
		t.Errorf("enclosures = %+v", es)
	}
	e, err := h.s.NameEnclosure(h.ctx, "expander-4:1", "storage1-back", "", "scott")
	if err != nil || e.Name != "storage1-back" || e.Key != "0x500304801ea12df0" || e.Product != "LSI SAS3x28" {
		t.Errorf("NameEnclosure = %+v, %v", e, err)
	}
	if e, err := h.s.NameEnclosure(h.ctx, "12df0", "", "", "scott"); err != nil || e.Name != "" {
		t.Errorf("clear by partial key = %+v, %v", e, err)
	}
}

// TestEnclosureProductFromAgent: an NVMe chassis enclosure carries its own
// model, since no SAS node reaches it.
func TestEnclosureProductFromAgent(t *testing.T) {
	h := newHarness(t)
	n := ReportDevice{DevName: "nvme0n1", Identity: DriveIdentity{WWN: "eui.1", Model: "SSDPD2KS", Serial: "N1"}, Bus: "nvme", SizeBytes: 7e12, Bay: "9-1",
		EnclosureID: "dmi:KCS0GX0000TB", EnclosureVia: "pci", EnclosureViaID: "dmi:KCS0GX0000TB", EnclosureModel: "ASUSTeK COMPUTER INC. RS500A-E10-RS12U", Uses: []string{"zfs > fast 1 > mirror 0 > disk 0"}}
	r := Report{Host: hostA, ObservedAt: h.now, Devices: []ReportDevice{n}, Complete: true,
		EmptyBays: []ReportBay{{EnclosureID: "dmi:KCS0GX0000TB", EnclosureVia: "pci", EnclosureViaID: "dmi:KCS0GX0000TB", EnclosureModel: "ASUSTeK COMPUTER INC. RS500A-E10-RS12U", Bay: "9"}}}
	h.submit(r)
	es, err := h.s.ListEnclosures(h.ctx)
	if err != nil || len(es) != 1 || es[0].Product != "ASUSTeK COMPUTER INC. RS500A-E10-RS12U" || es[0].Via != "pci" || es[0].Drives != 1 || es[0].Bays != 12 || es[0].Key != "dmi:KCS0GX0000TB" {
		t.Errorf("ListEnclosures = %+v, %v", es, err)
	}
	if e, err := h.s.NameEnclosure(h.ctx, "KCS0GX", "mgmt1-front", "", "scott"); err != nil || e.Name != "mgmt1-front" || e.Product == "" {
		t.Errorf("NameEnclosure = %+v, %v", e, err)
	}
	d, _, _, err := h.s.GetDrive(h.ctx, "N1")
	if err != nil || d.Current == nil || d.Current.EnclosureName != "mgmt1-front" || d.Current.Bay != "9-1" {
		t.Errorf("drive = %+v, %v", d.Current, err)
	}
}

// TestHardwareProfiles: an enclosure whose model has a profile shows the
// profile's bays and labels; occupied bays the profile does not know are
// listed after them; a model without a profile lists what is occupied.
func TestHardwareProfiles(t *testing.T) {
	h := newHarness(t)
	const shelf, exp = "0x5000ccab020094ff", "0x5000ccab0200947e"
	a := dev("sdbe", "A1", "0x5000000000000031", "expander-11:0", "54", "zfs > space 1 > raidz2 0 > disk 0")
	b := dev("sdbf", "B1", "0x5000000000000032", "expander-11:0", "62", "zfs > space 1 > raidz2 0 > disk 1")
	for _, d := range []*ReportDevice{&a, &b} {
		d.ExpanderID, d.EnclosureID, d.EnclosureVia, d.EnclosureViaID = exp, shelf, "expander-11:0", exp
	}
	n := ReportDevice{DevName: "nvme0n1", Identity: DriveIdentity{WWN: "eui.1", Model: "SSDPD2KS", Serial: "N1"}, Bus: "nvme", SizeBytes: 7e12, Bay: "9-1",
		EnclosureID: "dmi:KCS0GX0000TB", EnclosureVia: "pci", EnclosureViaID: "dmi:KCS0GX0000TB", EnclosureModel: "ASUSTeK COMPUTER INC. RS500A-E10-RS12U", Uses: []string{"zfs > fast 1 > mirror 0 > disk 0"}}
	r := Report{Host: hostA, ObservedAt: h.now, Devices: []ReportDevice{a, b, n}, Complete: true,
		SASNodes: []SASNode{{Kind: "expander", Name: "expander-11:0", Address: exp, Vendor: "HGST", Product: "4U60_STOR_ENCL", Revision: "0210"}}}
	h.submit(r)

	es, err := h.s.ListEnclosures(h.ctx)
	if err != nil || len(es) != 2 {
		t.Fatalf("ListEnclosures = %+v, %v", es, err)
	}
	byKey := map[string]Enclosure{}
	for _, e := range es {
		byKey[e.Key] = e
	}
	if e := byKey[shelf]; e.Profile == "" || e.Bays != 60 || e.Product != "HGST 4U60_STOR_ENCL" {
		t.Errorf("shelf = %+v", e)
	}
	if e := byKey["dmi:KCS0GX0000TB"]; e.Profile == "" || e.Bays != 12 {
		t.Errorf("chassis = %+v", e)
	}
	d, _, _, err := h.s.GetDrive(h.ctx, "A1")
	if err != nil || d.Current == nil || d.Current.BayLabel != "54" || d.Current.EnclosureModel != "HGST 4U60_STOR_ENCL" {
		t.Errorf("A1 placement = %+v, %v", d.Current, err)
	}
	if d, _, _, _ := h.s.GetDrive(h.ctx, "N1"); d.Current.BayLabel != "" {
		t.Errorf("unprobed RS500A labeled bay %q", d.Current.BayLabel)
	}

	enc, bays, layout, err := h.s.ListBays(h.ctx, "94ff")
	if err != nil || enc.Key != shelf || layout == nil || layout.Rows != 5 || len(bays) != 61 {
		t.Fatalf("ListBays = %+v, %d bays, layout %+v, %v", enc, len(bays), layout, err)
	}
	if b := bays[54]; !b.Declared || !b.Present || b.Serial != "A1" || b.Label != "54" || b.IDs[0] != "sas:54" {
		t.Errorf("bay 54 = %+v", b)
	}
	if b := bays[0]; !b.Declared || b.Present {
		t.Errorf("bay 0 = %+v", b)
	}
	if b := bays[60]; b.Declared || !b.Present || b.Serial != "B1" || b.Label != "62" || b.IDs[0] != "sas:62" {
		t.Errorf("undeclared bay = %+v", b)
	}
	_, bays, _, err = h.s.ListBays(h.ctx, "KCS0GX")
	if err != nil || len(bays) != 13 || bays[12].Declared || bays[12].Serial != "N1" || bays[12].IDs[0] != "pci:9-1" {
		t.Errorf("RS500A bays = %d, last %+v, %v", len(bays), bays[len(bays)-1], err)
	}
	models, _ := h.s.EnclosureModels(h.ctx)
	if models[shelf] != "HGST 4U60_STOR_ENCL" || h.s.BayLabel(models[shelf], "expander-11:0", "54") != "54" || h.s.BayLabel(models[shelf], "expander-11:0", "62") != "" {
		t.Errorf("EnclosureModels/BayLabel: %v", models)
	}
}
