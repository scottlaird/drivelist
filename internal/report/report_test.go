package report

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/scottlaird/drivelist"
	"github.com/scottlaird/drivelist/collect"
	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
)

func TestFromInventory(t *testing.T) {
	inv, err := collect.Fixture(filepath.Join("..", "..", "collect", "testdata", "synthetic")).Collect()
	if err != nil {
		t.Fatal(err)
	}
	host := &pb.HostIdentity{MachineId: "m", Hostname: "h"}
	now := time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC)
	req := FromInventory(host, inv, nil, now, nil)
	if !req.Complete || req.ObservedAt.AsTime() != now || req.Host != host {
		t.Errorf("envelope = %v", req)
	}
	// 5 devices (nvme0n1, sda, sdb, sdc, sdd) and 1 empty bay.
	if len(req.Devices) != 5 || len(req.EmptyBays) != 1 {
		t.Fatalf("devices=%d empty bays=%d", len(req.Devices), len(req.EmptyBays))
	}
	byName := map[string]*pb.Device{}
	for _, d := range req.Devices {
		byName[d.DevName] = d
	}
	sda := byName["sda"]
	if sda.Identity.Serial != "7SG3RM2G" || sda.Identity.Wwn != "0x5000cca25206c808" || sda.Bus != pb.Bus_BUS_SAS || sda.Bay != "0" || sda.ScsiAddr != "4:0:0:0" {
		t.Errorf("sda = %v", sda)
	}
	if len(sda.DevLinks) != 3 || sda.DevLinks[0] != "/dev/disk/by-id/wwn-0x5000cca25206c808" {
		t.Errorf("sda dev_links = %v", sda.DevLinks)
	}
	if got := byName["nvme0n1"].Bus; got != pb.Bus_BUS_NVME {
		t.Errorf("nvme0n1 bus = %v", got)
	}
	if got := byName["sdc"].Bus; got != pb.Bus_BUS_SATA {
		t.Errorf("sdc (ID_BUS=ata) bus = %v", got)
	}
	if byName["sdd"].Error == "" {
		t.Error("sdd should carry its identification error")
	}

	failed := FromInventory(host, nil, nil, now, errors.New("zpool: boom"))
	if failed.Complete || len(failed.CollectorErrors) != 1 || len(failed.Devices) != 0 {
		t.Errorf("failed report = %v", failed)
	}
}

func TestBusFromScsiATA(t *testing.T) {
	d := &drivelist.Device{Attribs: map[string]string{"ID_BUS": "scsi", "ID_ATA": "1"}}
	if got := bus(d); got != pb.Bus_BUS_SATA {
		t.Errorf("SATA behind SAS = %v", got)
	}
	d = &drivelist.Device{Attribs: map[string]string{"ID_BUS": "scsi"}}
	if got := bus(d); got != pb.Bus_BUS_SAS {
		t.Errorf("plain scsi = %v", got)
	}
}

func TestHost(t *testing.T) {
	h := Host()
	if h.Hostname == "" || h.MachineId == "" || h.Os == "" {
		t.Errorf("Host() = %v", h)
	}
	for name, want := range map[string]string{"fs2": "fs2", "scottstudio.internal.sigkill.org": "scottstudio", ".": ".", "": ""} {
		if got := shortHostname(name); got != want {
			t.Errorf("shortHostname(%q) = %q, want %q", name, got, want)
		}
	}
	if got := btime("cpu  1 2 3\nbtime 1757975138\nprocesses 5\n"); got.Unix() != 1757975138 {
		t.Errorf("btime = %v", got)
	}
	if got := btime("cpu 1\n"); !got.IsZero() {
		t.Errorf("btime without a line = %v", got)
	}
	if got := darwinBoottime("{ sec = 1757975138, usec = 412 } Mon Sep 15 14:45:38 2026\n"); got.Unix() != 1757975138 {
		t.Errorf("darwinBoottime = %v", got)
	}
	if got := darwinBoottime("garbage"); !got.IsZero() {
		t.Errorf("darwinBoottime garbage = %v", got)
	}
}

func TestSASFlatten(t *testing.T) {
	topo, err := collect.Fixture(filepath.Join("..", "..", "collect", "testdata", "synthetic")).SAS()
	if err != nil {
		t.Fatal(err)
	}
	nodes, phys := SAS(topo)
	if len(nodes) != 2 || nodes[1].ParentAddress != nodes[0].Address || nodes[1].UpstreamPort != "port-4:0" {
		t.Errorf("nodes = %+v", nodes)
	}
	if len(phys) != 20 {
		t.Fatalf("phys = %d, want 20", len(phys))
	}
	byName := map[string]*pb.SasPhy{}
	for _, p := range phys {
		byName[p.Name] = p
	}
	if p := byName["phy-4:0"]; p.AttachedKind != "expander" || p.Attached != "expander-4:0" || p.AttachedAddress != nodes[1].Address || p.PortWidth != 4 || p.RateGbit != 12 {
		t.Errorf("hba phy 0 = %+v", p)
	}
	if p := byName["phy-4:0:0"]; p.AttachedKind != "upstream" || p.Port != "" {
		t.Errorf("upstream phy = %+v", p)
	}
	if p := byName["phy-4:0:5"]; p.AttachedKind != "drive" || p.DevName != "sdb" || p.Bay != "1" || p.RateGbit != 6 || p.LossDwordSync != 7 || p.AttachedAddress != "0x5000cca260c165e3" {
		t.Errorf("sdb phy = %+v", p)
	}
	if p := byName["phy-4:0:6"]; p.AttachedKind != "device" || p.DevName != "" || p.Bay != "2" {
		t.Errorf("empty bay phy = %+v", p)
	}
	if p := byName["phy-4:7"]; p.AttachedKind != "" || p.RateGbit != 0 {
		t.Errorf("unattached phy = %+v", p)
	}
}
