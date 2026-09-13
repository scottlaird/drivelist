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
			DevName:        d.GetDevName(),
			Identity:       identityFromProto(d.GetIdentity()),
			Bus:            busNames[d.GetBus()],
			SizeBytes:      d.GetSizeBytes(),
			Expander:       d.GetExpander(),
			ExpanderID:     d.GetExpanderId(),
			Bay:            d.GetBay(),
			EnclosureID:    d.GetEnclosureId(),
			EnclosureVia:   d.GetEnclosureVia(),
			EnclosureViaID: d.GetEnclosureViaId(),
			EnclosurePath:  d.GetEnclosurePath(),
			Uses:           d.GetUses(),
			DevLinks:       d.GetDevLinks(),
			Error:          d.GetError(),
			MemberState:    d.GetMemberState(),
			SCSIAddr:       d.GetScsiAddr(),
		})
	}
	for _, m := range req.GetUnmappedMembers() {
		r.Unmapped = append(r.Unmapped, store.PoolMember{Pool: m.GetPool(), Path: m.GetPath(), GUID: m.GetGuid(), State: m.GetState()})
	}
	for _, b := range req.GetEmptyBays() {
		r.EmptyBays = append(r.EmptyBays, store.ReportBay{EnclosureID: b.GetEnclosureId(), EnclosureVia: b.GetEnclosureVia(), EnclosureViaID: b.GetEnclosureViaId(), Bay: b.GetBay()})
	}
	for _, n := range req.GetSasNodes() {
		r.SASNodes = append(r.SASNodes, sasNodeFromProto(n))
	}
	for _, p := range req.GetSasPhys() {
		r.SASPhys = append(r.SASPhys, sasPhyFromProto(p))
	}
	return r
}

func sasNodeFromProto(n *pb.SasNode) store.SASNode {
	return store.SASNode{Kind: n.GetKind(), Name: n.GetName(), Address: n.GetAddress(), Vendor: n.GetVendor(), Product: n.GetProduct(), Revision: n.GetRevision(),
		ParentAddress: n.GetParentAddress(), UpstreamPort: n.GetUpstreamPort()}
}

func sasNodeToProto(n store.SASNode) *pb.SasNode {
	return &pb.SasNode{Kind: n.Kind, Name: n.Name, Address: n.Address, Vendor: n.Vendor, Product: n.Product, Revision: n.Revision, ParentAddress: n.ParentAddress, UpstreamPort: n.UpstreamPort}
}

func sasPhyFromProto(p *pb.SasPhy) store.SASPhy {
	return store.SASPhy{OwnerAddress: p.GetOwnerAddress(), PhyID: int(p.GetPhyId()), Name: p.GetName(), Port: p.GetPort(), PortWidth: int(p.GetPortWidth()),
		Rate: p.GetRate(), RateGbit: p.GetRateGbit(), AttachedKind: p.GetAttachedKind(), Attached: p.GetAttached(), AttachedAddress: p.GetAttachedAddress(),
		DevName: p.GetDevName(), Bay: p.GetBay(), Enabled: p.GetEnabled(), InvalidDword: p.GetInvalidDword(), DisparityError: p.GetDisparityError(),
		LossDwordSync: p.GetLossDwordSync(), PhyResetProblem: p.GetPhyResetProblem()}
}

func sasPhyToProto(p store.SASPhy) *pb.SasPhy {
	return &pb.SasPhy{OwnerAddress: p.OwnerAddress, PhyId: uint32(p.PhyID), Name: p.Name, Port: p.Port, PortWidth: uint32(p.PortWidth), Rate: p.Rate, RateGbit: p.RateGbit,
		AttachedKind: p.AttachedKind, Attached: p.Attached, AttachedAddress: p.AttachedAddress, DevName: p.DevName, Bay: p.Bay, Enabled: p.Enabled,
		InvalidDword: p.InvalidDword, DisparityError: p.DisparityError, LossDwordSync: p.LossDwordSync, PhyResetProblem: p.PhyResetProblem}
}

func sasErrorRowToProto(r store.SASErrorRow) *pb.SasErrorRow {
	return &pb.SasErrorRow{Hostname: r.Hostname, OwnerName: r.OwnerName, OwnerAddress: r.OwnerAddress, PhyId: uint32(r.PhyID), Port: r.Port, AttachedKind: r.AttachedKind, Attached: r.Attached,
		DevName: r.DevName, Serial: r.Serial, Bay: r.Bay, InvalidDword: r.InvalidDword, DisparityError: r.DisparityError, LossDwordSync: r.LossDwordSync, PhyResetProblem: r.PhyResetProblem,
		LastAt: ts(r.LastAt), Samples: uint32(r.Samples)}
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
		Hostname:      p.Hostname,
		Enclosure:     p.Enclosure,
		EnclosureVia:  p.EnclosureVia,
		EnclosureName: p.EnclosureName,
		Bay:           p.Bay,
		DevName:       p.DevName,
		Uses:          p.Uses,
		FirstSeen:     ts(p.FirstSeen),
		LastSeen:      ts(p.LastSeen),
		EndedAt:       ts(p.EndedAt),
		EndReason:     p.EndReason,
	}
}

func enclosureToProto(e store.Enclosure) *pb.Enclosure {
	return &pb.Enclosure{Enclosure: e.Key, Hostname: e.Hostname, Via: e.Via, Product: e.Product, Name: e.Name, Note: e.Note, Drives: int32(e.Drives), Bays: int32(e.Bays), FirstSeen: ts(e.FirstSeen), LastSeen: ts(e.LastSeen)}
}

// nameEnclosures adds the names people gave enclosures to the events that
// mention one, as enclosure_name beside each key in the detail, so the
// client can print the name without another round trip. Events from
// before 0.7 carry the key as "expander".
func nameEnclosures(names map[string]string, evs []*pb.Event) {
	if len(names) == 0 {
		return
	}
	for _, e := range evs {
		var d map[string]any
		if json.Unmarshal([]byte(e.GetDetail()), &d) != nil {
			continue
		}
		changed := false
		for _, k := range []string{"enclosure", "from_enclosure", "to_enclosure", "expander", "from_expander", "to_expander", "from", "to"} {
			key, _ := d[k].(string)
			if n, ok := names[key]; ok && key != "" {
				d[k+"_name"] = n
				changed = true
			}
		}
		if changed {
			if b, err := json.Marshal(d); err == nil {
				e.Detail = string(b)
			}
		}
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

func kernelSamplesFromProto(samples []*pb.KernelSample) []store.KernelSample {
	out := make([]store.KernelSample, 0, len(samples))
	for _, k := range samples {
		s := store.KernelSample{
			Identity:   identityFromProto(k.GetIdentity()),
			DevName:    k.GetDevName(),
			BucketSecs: int(k.GetBucketSecs()),
			Class:      k.GetClass(),
			Code:       k.GetScsiCode(),
			Count:      int(k.GetCount()),
			Sample:     k.GetSample(),
		}
		if b := k.GetBucketStart(); b != nil {
			s.BucketStart = b.AsTime()
		}
		out = append(out, s)
	}
	return out
}

func kernelSampleToProto(k store.KernelSample) *pb.KernelSample {
	return &pb.KernelSample{
		Identity:    identityToProto(k.Identity),
		DevName:     k.DevName,
		BucketStart: ts(k.BucketStart),
		BucketSecs:  uint32(k.BucketSecs),
		Class:       k.Class,
		ScsiCode:    k.Code,
		Count:       uint32(k.Count),
		Sample:      k.Sample,
		Hostname:    k.Hostname,
	}
}

func smartSummaryFromProto(m *pb.SmartSummary) *store.SmartSummary {
	if m == nil {
		return nil
	}
	s := &store.SmartSummary{Protocol: m.GetProtocol(), SelftestLast: m.GetSelftestLast()}
	if m.Healthy != nil {
		s.Healthy = m.Healthy
	}
	s.PowerOnHours, s.Reallocated, s.Pending, s.Uncorrectable = m.PowerOnHours, m.Reallocated, m.Pending, m.Uncorrectable
	s.CRCErrors, s.ReadBytes, s.WriteBytes, s.PercentUsed = m.CrcErrors, m.ReadBytes, m.WriteBytes, m.PercentUsed
	if m.TempC != nil {
		v := int(*m.TempC)
		s.TempC = &v
	}
	return s
}

func smartSummaryToProto(s *store.SmartSummary) *pb.SmartSummary {
	if s == nil {
		return nil
	}
	m := &pb.SmartSummary{Protocol: s.Protocol, SelftestLast: s.SelftestLast, Healthy: s.Healthy,
		PowerOnHours: s.PowerOnHours, Reallocated: s.Reallocated, Pending: s.Pending, Uncorrectable: s.Uncorrectable,
		CrcErrors: s.CRCErrors, ReadBytes: s.ReadBytes, WriteBytes: s.WriteBytes, PercentUsed: s.PercentUsed}
	if s.TempC != nil {
		v := int32(*s.TempC)
		m.TempC = &v
	}
	return m
}

func smartSamplesFromProto(samples []*pb.SmartSample) []store.SmartSample {
	out := make([]store.SmartSample, 0, len(samples))
	for _, k := range samples {
		s := store.SmartSample{Identity: identityFromProto(k.GetIdentity()), DevName: k.GetDevName(), Summary: smartSummaryFromProto(k.GetSummary()), RawGz: k.GetRawJsonGz(), Skipped: k.GetSkipped()}
		if t := k.GetTs(); t != nil {
			s.TS = t.AsTime()
		}
		out = append(out, s)
	}
	return out
}

func smartSampleToProto(k store.SmartSample) *pb.SmartSample {
	return &pb.SmartSample{Identity: identityToProto(k.Identity), DevName: k.DevName, Ts: ts(k.TS), Summary: smartSummaryToProto(k.Summary), Skipped: k.Skipped, Hostname: k.Hostname, HasRaw: k.HasRaw}
}

func ioSamplesFromProto(samples []*pb.IOSample) []store.IOSample {
	out := make([]store.IOSample, 0, len(samples))
	for _, k := range samples {
		s := store.IOSample{Identity: identityFromProto(k.GetIdentity()), DevName: k.GetDevName(), BucketSecs: int(k.GetBucketSecs()),
			Reads: k.GetReads(), Writes: k.GetWrites(), ReadBytes: k.GetReadBytes(), WriteBytes: k.GetWriteBytes(),
			ReadMs: k.GetReadMs(), WriteMs: k.GetWriteMs(), IOMs: k.GetIoMs(), WeightedMs: k.GetWeightedMs(),
			RAwaitMax: k.GetRAwaitMaxMs(), WAwaitMax: k.GetWAwaitMaxMs(), UtilMax: k.GetUtilMax(),
			AwaitReads: k.GetAwaitReads(), AwaitWrites: k.GetAwaitWrites(), Glitches: int(k.GetGlitches())}
		if b := k.GetBucketStart(); b != nil {
			s.BucketStart = b.AsTime()
		}
		out = append(out, s)
	}
	return out
}

func ioSampleToProto(k store.IOSample) *pb.IOSample {
	return &pb.IOSample{Identity: identityToProto(k.Identity), DevName: k.DevName, BucketStart: ts(k.BucketStart), BucketSecs: uint32(k.BucketSecs),
		Reads: k.Reads, Writes: k.Writes, ReadBytes: k.ReadBytes, WriteBytes: k.WriteBytes, ReadMs: k.ReadMs, WriteMs: k.WriteMs, IoMs: k.IOMs, WeightedMs: k.WeightedMs,
		RAwaitMaxMs: k.RAwaitMax, WAwaitMaxMs: k.WAwaitMax, UtilMax: k.UtilMax, Hostname: k.Hostname,
		AwaitReads: k.AwaitReads, AwaitWrites: k.AwaitWrites, Glitches: uint32(k.Glitches)}
}

func ioComparisonToProto(c store.IOComparison) *pb.IOComparison {
	return &pb.IOComparison{Group: c.Group, Hostname: c.Hostname, Serial: c.Serial, Model: c.Model, DevName: c.DevName, Reads: c.Reads, Writes: c.Writes,
		RAwaitMs: c.RAwait, WAwaitMs: c.WAwait, Util: c.Util, GroupRAwaitMs: c.GroupRAwait, GroupWAwaitMs: c.GroupWAwait, GroupUtil: c.GroupUtil, GroupSize: int32(c.GroupSize), Glitches: uint32(c.Glitches)}
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
