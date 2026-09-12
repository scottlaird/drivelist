package server

import (
	"encoding/json"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
	"github.com/scottlaird/drivelist/internal/store"
)

// This file maps between the wire types and the store's types. The store
// never imports the proto package, so the API can move without the
// database noticing and vice versa.

var busNames = map[pb.Bus]string{
	pb.Bus_BUS_SAS:    "sas",
	pb.Bus_BUS_SATA:   "sata",
	pb.Bus_BUS_NVME:   "nvme",
	pb.Bus_BUS_USB:    "usb",
	pb.Bus_BUS_VIRTIO: "virtio",
}

var busValues = func() map[string]pb.Bus {
	m := make(map[string]pb.Bus, len(busNames))
	for k, v := range busNames {
		m[v] = k
	}
	return m
}()

func reportFromProto(req *pb.ReportInventoryRequest) store.Report {
	r := store.Report{
		Host:            hostIdentityFromProto(req.GetHost()),
		Complete:        req.GetComplete(),
		CollectorErrors: req.GetCollectorErrors(),
	}
	if ts := req.GetObservedAt(); ts != nil {
		r.ObservedAt = ts.AsTime()
	}
	for _, d := range req.GetDevices() {
		r.Devices = append(r.Devices, store.ReportDevice{
			DevName:       d.GetDevName(),
			Identity:      identityFromProto(d.GetIdentity()),
			Bus:           busNames[d.GetBus()],
			SizeBytes:     d.GetSizeBytes(),
			Expander:      d.GetExpander(),
			Bay:           d.GetBay(),
			EnclosurePath: d.GetEnclosurePath(),
			Uses:          d.GetUses(),
			DevLinks:      d.GetDevLinks(),
			Error:         d.GetError(),
			MemberState:   d.GetMemberState(),
			SCSIAddr:      d.GetScsiAddr(),
		})
	}
	for _, m := range req.GetUnmappedMembers() {
		r.Unmapped = append(r.Unmapped, store.PoolMember{Pool: m.GetPool(), Path: m.GetPath(), GUID: m.GetGuid(), State: m.GetState()})
	}
	return r
}

func hostIdentityFromProto(h *pb.HostIdentity) store.HostIdentity {
	return store.HostIdentity{MachineID: h.GetMachineId(), Hostname: h.GetHostname(), OS: h.GetOs(), AgentVersion: h.GetAgentVersion()}
}

func identityFromProto(id *pb.DriveIdentity) store.DriveIdentity {
	return store.DriveIdentity{WWN: id.GetWwn(), Vendor: id.GetVendor(), Model: id.GetModel(), Serial: id.GetSerial()}
}

func identityToProto(id store.DriveIdentity) *pb.DriveIdentity {
	return &pb.DriveIdentity{Wwn: id.WWN, Vendor: id.Vendor, Model: id.Model, Serial: id.Serial}
}

func hostToProto(h store.Host) *pb.Host {
	return &pb.Host{
		Hostname:     h.Hostname,
		MachineId:    h.MachineID,
		Os:           h.OS,
		AgentVersion: h.AgentVersion,
		FirstSeen:    ts(h.FirstSeen),
		LastReport:   ts(h.LastReport),
		StaleSince:   ts(h.StaleSince),
		DriveCount:   int32(h.DriveCount),
		MissingCount: int32(h.MissingCount),
		GhostCount:   int32(h.GhostCount),
	}
}

func placementToProto(p *store.Placement) *pb.Placement {
	if p == nil {
		return nil
	}
	return &pb.Placement{
		Hostname:  p.Hostname,
		Expander:  p.Expander,
		Bay:       p.Bay,
		DevName:   p.DevName,
		Uses:      p.Uses,
		FirstSeen: ts(p.FirstSeen),
		LastSeen:  ts(p.LastSeen),
		EndedAt:   ts(p.EndedAt),
		EndReason: p.EndReason,
	}
}

func driveToProto(d store.Drive) *pb.Drive {
	return &pb.Drive{
		DriveId:     d.ID,
		Wwn:         d.WWN,
		Vendor:      d.Vendor,
		Model:       d.Model,
		Serial:      d.Serial,
		SizeBytes:   d.SizeBytes,
		Bus:         busValues[d.Bus],
		Status:      d.Status,
		FirstSeen:   ts(d.FirstSeen),
		LastSeen:    ts(d.LastSeen),
		Current:     placementToProto(d.Current),
		Last:        placementToProto(d.Last),
		MemberState: d.MemberState,
	}
}

func eventToProto(e store.Event) *pb.Event {
	detail := e.Detail
	if !json.Valid([]byte(detail)) {
		detail = "{}"
	}
	return &pb.Event{
		EventId:  e.ID,
		Ts:       ts(e.TS),
		Kind:     e.Kind,
		Serial:   e.Serial,
		Wwn:      e.WWN,
		Hostname: e.Hostname,
		Detail:   detail,
		Source:   e.Source,
	}
}

func ghostToProto(g store.Ghost) *pb.Ghost {
	return &pb.Ghost{
		Hostname:  g.Hostname,
		Pool:      g.Pool,
		Path:      g.Path,
		Guid:      g.GUID,
		State:     g.State,
		Serial:    g.Serial,
		Wwn:       g.WWN,
		FirstSeen: ts(g.FirstSeen),
		LastSeen:  ts(g.LastSeen),
	}
}

// ts converts a time to a proto timestamp, nil for the zero time.
func ts(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
