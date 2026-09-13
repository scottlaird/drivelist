// Package report turns a collected inventory into the wire form an agent
// sends, and identifies the host it came from. The one-shot report command
// and the agent both use it.
package report

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/scottlaird/drivelist"
	"github.com/scottlaird/drivelist/collect"
	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
)

// Version is stamped into every report's agent_version; the build sets it.
var Version = "dev"

// Host identifies this machine. The machine id is /etc/machine-id on
// Linux and the IOPlatformUUID on macOS; elsewhere, or when neither can be
// read, the hostname stands in, which means a rename forks the history.
func Host() *pb.HostIdentity {
	hostname, _ := os.Hostname()
	return &pb.HostIdentity{
		MachineId:    machineID(hostname),
		Hostname:     hostname,
		Os:           runtime.GOOS,
		AgentVersion: Version,
	}
}

func machineID(fallback string) string {
	switch runtime.GOOS {
	case "linux":
		if b, err := os.ReadFile("/etc/machine-id"); err == nil {
			if id := strings.TrimSpace(string(b)); id != "" {
				return id
			}
		}
	case "darwin":
		out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		if err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, "IOPlatformUUID") {
					if _, v, ok := strings.Cut(line, "= "); ok {
						return strings.Trim(strings.TrimSpace(v), `"`)
					}
				}
			}
		}
	}
	return "hostname:" + fallback
}

// FromInventory builds the report for inv, observed at now. It is complete
// unless collectErr is non-nil, in which case the error is carried in
// collector_errors and the server treats the report as untrustworthy.
// topo, when not nil, rides along as the SAS topology.
func FromInventory(host *pb.HostIdentity, inv *drivelist.Inventory, topo *collect.SASTopology, now time.Time, collectErr error) *pb.ReportInventoryRequest {
	req := &pb.ReportInventoryRequest{
		Host:       host,
		ObservedAt: timestamppb.New(now),
		Complete:   collectErr == nil,
	}
	if collectErr != nil {
		req.CollectorErrors = []string{collectErr.Error()}
	}
	if inv == nil {
		return req
	}
	for _, d := range inv.Devices {
		if d.IsEmptyBay() {
			req.EmptyBays = append(req.EmptyBays, &pb.EmptyBay{Expander: d.Expander, ExpanderId: d.ExpanderID, Bay: d.EnclosureBay, EnclosurePath: d.ExpanderPath,
				EnclosureId: d.EnclosureID, EnclosureVia: d.EnclosureVia, EnclosureViaId: d.EnclosureViaID, EnclosureModel: d.EnclosureModel})
			continue
		}
		req.Devices = append(req.Devices, Device(d))
	}
	for _, m := range inv.Unmapped {
		req.UnmappedMembers = append(req.UnmappedMembers, &pb.UnmappedMember{Pool: m.Pool, Path: m.Path, Guid: m.GUID, State: m.State})
	}
	req.SasNodes, req.SasPhys = SAS(topo)
	return req
}

// SAS flattens a topology for the wire: one node per HBA or expander, and
// every phy with what its port leads to, so the server can diff phys
// without walking a tree.
func SAS(topo *collect.SASTopology) ([]*pb.SasNode, []*pb.SasPhy) {
	if topo == nil {
		return nil, nil
	}
	addrOf := map[string]string{}
	for _, n := range topo.Nodes {
		addrOf[n.Name] = n.Address
	}
	var nodes []*pb.SasNode
	var phys []*pb.SasPhy
	for _, n := range topo.Nodes {
		nodes = append(nodes, &pb.SasNode{Kind: n.Kind, Name: n.Name, Address: n.Address, Vendor: n.Vendor, Product: n.Product, Revision: n.Revision,
			ParentAddress: addrOf[n.Parent], UpstreamPort: n.Upstream})
		for _, p := range n.Phys {
			w := &pb.SasPhy{OwnerAddress: n.Address, PhyId: uint32(p.ID), Name: p.Name, Port: p.Port, Rate: p.Rate, RateGbit: p.Gbit(), Enabled: p.Enabled,
				InvalidDword: p.InvalidDword, DisparityError: p.DisparityErr, LossDwordSync: p.LossDwordSync, PhyResetProblem: p.ResetProblem}
			switch port := n.Port(p.Port); {
			case port == nil:
				if p.Up() && n.Kind == "expander" {
					w.AttachedKind = "upstream"
				}
			case strings.HasPrefix(port.Attached, "expander-"):
				w.PortWidth, w.AttachedKind, w.Attached, w.AttachedAddress = uint32(len(port.Phys)), "expander", port.Attached, addrOf[port.Attached]
			default:
				w.PortWidth, w.AttachedKind, w.Attached = uint32(len(port.Phys)), "device", port.Attached
				for _, d := range n.Devices {
					if d.Name == port.Attached {
						w.AttachedAddress, w.Bay, w.DevName = d.Address, d.Bay, d.DevName
						if d.DevName != "" {
							w.AttachedKind = "drive"
						}
					}
				}
			}
			phys = append(phys, w)
		}
	}
	return nodes, phys
}

// Device converts one collected device.
func Device(d *drivelist.Device) *pb.Device {
	var links []string
	for _, name := range d.Devices {
		if strings.HasPrefix(name, "/dev/disk/") {
			links = append(links, name)
		}
	}
	return &pb.Device{
		DevName:        d.DeviceName,
		Identity:       &pb.DriveIdentity{Wwn: d.WWN, Vendor: d.Attribs["ID_VENDOR"], Model: d.Model, Serial: d.Serial},
		Bus:            bus(d),
		SizeBytes:      d.Size,
		Expander:       d.Expander,
		ExpanderId:     d.ExpanderID,
		Bay:            d.EnclosureBay,
		EnclosureId:    d.EnclosureID,
		EnclosureVia:   d.EnclosureVia,
		EnclosureViaId: d.EnclosureViaID,
		EnclosureModel: d.EnclosureModel,
		EnclosurePath:  d.ExpanderPath,
		Uses:           d.Uses,
		DevLinks:       links,
		Error:          d.Error,
		MemberState:    d.MemberState,
		ScsiAddr:       scsiAddr(d.SysPath),
	}
}

// bus classifies the transport from udev's ID_BUS and the sysfs path. A
// SATA drive behind a SAS expander reports ID_BUS=scsi like a SAS drive
// does; ID_ATA tells them apart.
func bus(d *drivelist.Device) pb.Bus {
	switch d.Attribs["ID_BUS"] {
	case "nvme":
		return pb.Bus_BUS_NVME
	case "usb":
		return pb.Bus_BUS_USB
	case "ata":
		return pb.Bus_BUS_SATA
	case "scsi":
		if d.Attribs["ID_ATA"] == "1" {
			return pb.Bus_BUS_SATA
		}
		return pb.Bus_BUS_SAS
	}
	switch {
	case strings.HasPrefix(d.DeviceName, "nvme"):
		return pb.Bus_BUS_NVME
	case strings.Contains(d.SysPath, "/virtio"):
		return pb.Bus_BUS_VIRTIO
	}
	return pb.Bus_BUS_UNSPECIFIED
}

// scsiAddr extracts the H:C:T:L address from a sysfs path such as
// …/target11:0:17/11:0:17:0/block/sdag, or "" when there is none.
func scsiAddr(sysPath string) string {
	parts := strings.Split(sysPath, "/")
	for i := len(parts) - 1; i > 0; i-- {
		if parts[i] == "block" && strings.Count(parts[i-1], ":") == 3 {
			return parts[i-1]
		}
	}
	return ""
}
