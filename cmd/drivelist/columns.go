package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/scottlaird/drivelist/hardware"
	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
)

// The columns of each listing. Sort keys are given where the printed
// text does not sort the way a person expects: byte counts, times,
// counters printed as "-" when unknown.

var hostCols = []col[*pb.Host]{
	{name: "host", header: "HOST", value: func(h *pb.Host) string { return h.Hostname }},
	{name: "drives", header: "DRIVES", value: func(h *pb.Host) string { return fmt.Sprint(h.DriveCount) }, key: func(h *pb.Host) any { return h.DriveCount }},
	{name: "missing", header: "MISSING", value: func(h *pb.Host) string { return fmt.Sprint(h.MissingCount) }, key: func(h *pb.Host) any { return h.MissingCount }},
	{name: "ghosts", header: "GHOSTS", value: func(h *pb.Host) string { return fmt.Sprint(h.GhostCount) }, key: func(h *pb.Host) any { return h.GhostCount }},
	{name: "last", header: "LAST REPORT", value: func(h *pb.Host) string { return ago(h.LastReport, time.Now()) }, key: func(h *pb.Host) any { return tsKey(h.LastReport) }},
	{name: "state", header: "STATE", value: func(h *pb.Host) string {
		if h.StaleSince != nil {
			return "stale since " + when(h.StaleSince)
		}
		return "ok"
	}},
	{name: "agent", header: "AGENT", value: func(h *pb.Host) string { return orDash(h.AgentVersion) }},
	{name: "up", header: "UP", value: func(h *pb.Host) string {
		if h.BootedAt == nil {
			return "-"
		}
		return gap(time.Since(h.BootedAt.AsTime()).Seconds())
	}, key: func(h *pb.Host) any { return tsKey(h.BootedAt) }},
	{name: "machineid", header: "MACHINE ID", value: func(h *pb.Host) string { return h.MachineId }, extra: true},
	{name: "os", header: "OS", value: func(h *pb.Host) string { return orDash(h.Os) }, extra: true},
	{name: "first", header: "FIRST SEEN", value: func(h *pb.Host) string { return when(h.FirstSeen) }, key: func(h *pb.Host) any { return tsKey(h.FirstSeen) }, extra: true},
}

// placementOf is the placement a drive listing describes: the current
// one, else the last, in which case the host is parenthesised.
func placementOf(d *pb.Drive) (p *pb.Placement, host string) {
	switch {
	case d.Current != nil:
		return d.Current, d.Current.Hostname
	case d.Last != nil:
		return d.Last, "(" + d.Last.Hostname + ")"
	}
	return nil, "-"
}

var driveCols = []col[*pb.Drive]{
	{name: "host", header: "HOST", value: func(d *pb.Drive) string { _, h := placementOf(d); return h }},
	{name: "slot", header: "SLOT", value: func(d *pb.Drive) string { p, _ := placementOf(d); return slotOf(p) }},
	{name: "device", header: "DEVICE", value: func(d *pb.Drive) string {
		if d.Current == nil {
			return "-"
		}
		return orDash(d.Current.DevName)
	}},
	{name: "model", header: "MODEL", value: func(d *pb.Drive) string { return d.Model }},
	{name: "serial", header: "SERIAL", value: func(d *pb.Drive) string { return d.Serial }},
	{name: "size", header: "SIZE", value: func(d *pb.Drive) string { return size(d.SizeBytes) }, key: func(d *pb.Drive) any { return d.SizeBytes }},
	{name: "bus", header: "BUS", value: func(d *pb.Drive) string { return busShort[d.Bus] }},
	{name: "status", header: "STATUS", value: func(d *pb.Drive) string { return d.Status }},
	{name: "zfs", header: "ZFS", value: func(d *pb.Drive) string { return orDash(d.MemberState) }},
	{name: "uses", header: "USES", value: func(d *pb.Drive) string {
		p, _ := placementOf(d)
		if p == nil {
			return "-"
		}
		return useSummary(p.Uses)
	}},
	{name: "wwn", header: "WWN", value: func(d *pb.Drive) string { return orDash(d.Wwn) }, extra: true},
	{name: "vendor", header: "VENDOR", value: func(d *pb.Drive) string { return orDash(d.Vendor) }, extra: true},
	{name: "enclosure", header: "ENCLOSURE", value: func(d *pb.Drive) string {
		p, _ := placementOf(d)
		if p == nil {
			return "-"
		}
		return orDash(p.Enclosure)
	}, extra: true},
	{name: "bay", header: "BAY", value: func(d *pb.Drive) string {
		p, _ := placementOf(d)
		if p == nil {
			return "-"
		}
		return orDash(p.Bay)
	}, extra: true},
	{name: "since", header: "SINCE", value: func(d *pb.Drive) string {
		p, _ := placementOf(d)
		if p == nil {
			return "-"
		}
		return when(p.FirstSeen)
	}, key: func(d *pb.Drive) any {
		p, _ := placementOf(d)
		if p == nil {
			return nil
		}
		return tsKey(p.FirstSeen)
	}, extra: true},
	{name: "firstseen", header: "FIRST SEEN", value: func(d *pb.Drive) string { return when(d.FirstSeen) }, key: func(d *pb.Drive) any { return tsKey(d.FirstSeen) }, extra: true},
	{name: "lastseen", header: "LAST SEEN", value: func(d *pb.Drive) string { return when(d.LastSeen) }, key: func(d *pb.Drive) any { return tsKey(d.LastSeen) }, extra: true},
}

// missingCols are the drives of `missing`: each has a last placement.
var missingCols = []col[*pb.Drive]{
	{name: "serial", header: "SERIAL", value: func(d *pb.Drive) string { return d.Serial }},
	{name: "model", header: "MODEL", value: func(d *pb.Drive) string { return d.Model }},
	{name: "status", header: "STATUS", value: func(d *pb.Drive) string { return d.Status }},
	{name: "host", header: "LAST HOST", value: func(d *pb.Drive) string { return d.Last.Hostname }},
	{name: "slot", header: "LAST SLOT", value: func(d *pb.Drive) string { return slotOf(d.Last) }},
	{name: "uses", header: "LAST USES", value: func(d *pb.Drive) string { return useSummary(d.Last.Uses) }},
	{name: "confirmed", header: "LAST CONFIRMED", value: func(d *pb.Drive) string { return when(d.Last.LastSeen) }, key: func(d *pb.Drive) any { return tsKey(d.Last.LastSeen) }},
	{name: "gone", header: "NOTICED GONE", value: func(d *pb.Drive) string { return when(d.Last.EndedAt) }, key: func(d *pb.Drive) any { return tsKey(d.Last.EndedAt) }},
	{name: "size", header: "SIZE", value: func(d *pb.Drive) string { return size(d.SizeBytes) }, key: func(d *pb.Drive) any { return d.SizeBytes }, extra: true},
	{name: "wwn", header: "WWN", value: func(d *pb.Drive) string { return orDash(d.Wwn) }, extra: true},
}

func smartSummary(r *pb.SmartRow) *pb.SmartSummary {
	if r.Sample == nil {
		return nil
	}
	return r.Sample.Summary
}

func smartOpt(f func(*pb.SmartSummary) *uint64) func(*pb.SmartRow) string {
	return func(r *pb.SmartRow) string {
		if m := smartSummary(r); m != nil {
			return optU(f(m))
		}
		return ""
	}
}

func smartOptKey(f func(*pb.SmartSummary) *uint64) func(*pb.SmartRow) any {
	return func(r *pb.SmartRow) any {
		if m := smartSummary(r); m != nil {
			return optKey(f(m))
		}
		return nil
	}
}

var smartCols = []col[*pb.SmartRow]{
	{name: "host", header: "HOST", value: func(r *pb.SmartRow) string { return r.Drive.Current.Hostname }},
	{name: "slot", header: "SLOT", value: func(r *pb.SmartRow) string { return slotOf(r.Drive.Current) }},
	{name: "device", header: "DEVICE", value: func(r *pb.SmartRow) string { return orDash(r.Drive.Current.DevName) }},
	{name: "serial", header: "SERIAL", value: func(r *pb.SmartRow) string { return r.Drive.Serial }},
	{name: "model", header: "MODEL", value: func(r *pb.SmartRow) string { return r.Drive.Model }},
	{name: "health", header: "HEALTH", value: func(r *pb.SmartRow) string {
		if r.Sample == nil {
			if r.LastSkipped != "" {
				return "skipped: " + r.LastSkipped
			}
			return "no reading"
		}
		return health(r.Sample.Summary)
	}, key: func(r *pb.SmartRow) any { return r.Problem }},
	{name: "hours", header: "HOURS", value: smartOpt(func(m *pb.SmartSummary) *uint64 { return m.PowerOnHours }), key: smartOptKey(func(m *pb.SmartSummary) *uint64 { return m.PowerOnHours })},
	{name: "temp", header: "TEMP", value: func(r *pb.SmartRow) string {
		if m := smartSummary(r); m != nil {
			return optTemp(m.TempC)
		}
		return ""
	}, key: func(r *pb.SmartRow) any {
		if m := smartSummary(r); m != nil && m.TempC != nil {
			return float64(*m.TempC)
		}
		return nil
	}},
	{name: "realloc", header: "REALLOC", value: smartOpt(func(m *pb.SmartSummary) *uint64 { return m.Reallocated }), key: smartOptKey(func(m *pb.SmartSummary) *uint64 { return m.Reallocated })},
	{name: "pending", header: "PENDING", value: smartOpt(func(m *pb.SmartSummary) *uint64 { return m.Pending }), key: smartOptKey(func(m *pb.SmartSummary) *uint64 { return m.Pending })},
	{name: "uncorr", header: "UNCORR", value: smartOpt(func(m *pb.SmartSummary) *uint64 { return m.Uncorrectable }), key: smartOptKey(func(m *pb.SmartSummary) *uint64 { return m.Uncorrectable })},
	{name: "crc", header: "CRC", value: smartOpt(func(m *pb.SmartSummary) *uint64 { return m.CrcErrors }), key: smartOptKey(func(m *pb.SmartSummary) *uint64 { return m.CrcErrors })},
	{name: "wear", header: "WEAR", value: func(r *pb.SmartRow) string {
		if m := smartSummary(r); m != nil {
			return optPct(m.PercentUsed)
		}
		return ""
	}, key: func(r *pb.SmartRow) any {
		if m := smartSummary(r); m != nil && m.PercentUsed != nil {
			return float64(*m.PercentUsed)
		}
		return nil
	}},
	{name: "selftest", header: "SELF-TEST", value: func(r *pb.SmartRow) string {
		if m := smartSummary(r); m != nil {
			return orDash(m.SelftestLast)
		}
		return ""
	}},
	{name: "sampled", header: "SAMPLED", value: func(r *pb.SmartRow) string {
		if r.Sample == nil {
			return ""
		}
		s := ago(r.Sample.Ts, time.Now())
		if r.LastSkipped != "" {
			s += " (now " + r.LastSkipped + ")"
		}
		return s
	}, key: func(r *pb.SmartRow) any {
		if r.Sample == nil {
			return nil
		}
		return tsKey(r.Sample.Ts)
	}},
	{name: "read", header: "READ", value: func(r *pb.SmartRow) string {
		if m := smartSummary(r); m != nil {
			return optBytes(m.ReadBytes)
		}
		return ""
	}, key: smartOptKey(func(m *pb.SmartSummary) *uint64 { return m.ReadBytes }), extra: true},
	{name: "written", header: "WRITTEN", value: func(r *pb.SmartRow) string {
		if m := smartSummary(r); m != nil {
			return optBytes(m.WriteBytes)
		}
		return ""
	}, key: smartOptKey(func(m *pb.SmartSummary) *uint64 { return m.WriteBytes }), extra: true},
	{name: "protocol", header: "PROTOCOL", value: func(r *pb.SmartRow) string {
		if m := smartSummary(r); m != nil {
			return orDash(m.Protocol)
		}
		return ""
	}, extra: true},
	{name: "problem", header: "PROBLEM", value: func(r *pb.SmartRow) string { return fmt.Sprint(r.Problem) }, key: func(r *pb.SmartRow) any { return r.Problem }, extra: true},
	{name: "status", header: "STATUS", value: func(r *pb.SmartRow) string { return r.Drive.Status }, extra: true},
}

var enclosureCols = []col[*pb.Enclosure]{
	{name: "host", header: "HOST", value: func(e *pb.Enclosure) string { return e.Hostname }},
	{name: "via", header: "VIA", value: func(e *pb.Enclosure) string { return orDash(e.Via) }},
	{name: "model", header: "MODEL", value: func(e *pb.Enclosure) string { return orDash(e.Product) }},
	{name: "name", header: "NAME", value: func(e *pb.Enclosure) string { return orDash(e.Name) }},
	{name: "drives", header: "DRIVES", value: func(e *pb.Enclosure) string { return fmt.Sprint(e.Drives) }, key: func(e *pb.Enclosure) any { return e.Drives }},
	{name: "bays", header: "BAYS", value: func(e *pb.Enclosure) string { return count(uint32(e.Bays)) }, key: func(e *pb.Enclosure) any { return e.Bays }},
	{name: "key", header: "KEY", value: func(e *pb.Enclosure) string { return e.Enclosure }},
	{name: "note", header: "NOTE", value: func(e *pb.Enclosure) string { return e.Note }},
	{name: "profile", header: "PROFILE", value: func(e *pb.Enclosure) string { return orDash(e.Profile) }, extra: true},
	{name: "board", header: "BOARD", value: func(e *pb.Enclosure) string { return orDash(e.Board) }, extra: true},
	{name: "first", header: "FIRST SEEN", value: func(e *pb.Enclosure) string { return when(e.FirstSeen) }, key: func(e *pb.Enclosure) any { return tsKey(e.FirstSeen) }, extra: true},
	{name: "last", header: "LAST SEEN", value: func(e *pb.Enclosure) string { return when(e.LastSeen) }, key: func(e *pb.Enclosure) any { return tsKey(e.LastSeen) }, extra: true},
}

var ioCols = []col[*pb.IOComparison]{
	{name: "vdev", header: "VDEV", value: func(r *pb.IOComparison) string { return groupLabel(r.Group, int(r.GroupSize)) }},
	{name: "host", header: "HOST", value: func(r *pb.IOComparison) string { return r.Hostname }},
	{name: "device", header: "DEVICE", value: func(r *pb.IOComparison) string { return r.DevName }},
	{name: "serial", header: "SERIAL", value: func(r *pb.IOComparison) string { return r.Serial }},
	{name: "model", header: "MODEL", value: func(r *pb.IOComparison) string { return r.Model }},
	{name: "r_await", header: "R_AWAIT", value: func(r *pb.IOComparison) string { return ms(r.RAwaitMs) }, key: func(r *pb.IOComparison) any { return r.RAwaitMs }},
	{name: "r_ratio", header: "vs MED", value: func(r *pb.IOComparison) string { return times(r.RAwaitMs, r.GroupRAwaitMs) }, key: func(r *pb.IOComparison) any { return ratio(r.RAwaitMs, r.GroupRAwaitMs) }},
	{name: "w_await", header: "W_AWAIT", value: func(r *pb.IOComparison) string { return ms(r.WAwaitMs) }, key: func(r *pb.IOComparison) any { return r.WAwaitMs }},
	{name: "w_ratio", header: "vs MED", value: func(r *pb.IOComparison) string { return times(r.WAwaitMs, r.GroupWAwaitMs) }, key: func(r *pb.IOComparison) any { return ratio(r.WAwaitMs, r.GroupWAwaitMs) }},
	{name: "util", header: "UTIL", value: func(r *pb.IOComparison) string { return pct(r.Util) }, key: func(r *pb.IOComparison) any { return r.Util }},
	{name: "util_ratio", header: "vs MED", value: func(r *pb.IOComparison) string { return times(r.Util, r.GroupUtil) }, key: func(r *pb.IOComparison) any { return ratio(r.Util, r.GroupUtil) }},
	{name: "reads", header: "READS", value: func(r *pb.IOComparison) string { return fmt.Sprint(r.Reads) }, key: func(r *pb.IOComparison) any { return r.Reads }},
	{name: "writes", header: "WRITES", value: func(r *pb.IOComparison) string { return fmt.Sprint(r.Writes) }, key: func(r *pb.IOComparison) any { return r.Writes }},
	{name: "r_med", header: "R_MED", value: func(r *pb.IOComparison) string { return ms(r.GroupRAwaitMs) }, key: func(r *pb.IOComparison) any { return r.GroupRAwaitMs }, extra: true},
	{name: "w_med", header: "W_MED", value: func(r *pb.IOComparison) string { return ms(r.GroupWAwaitMs) }, key: func(r *pb.IOComparison) any { return r.GroupWAwaitMs }, extra: true},
	{name: "util_med", header: "UTIL_MED", value: func(r *pb.IOComparison) string { return pct(r.GroupUtil) }, key: func(r *pb.IOComparison) any { return r.GroupUtil }, extra: true},
	{name: "dropped", header: "DROPPED", value: func(r *pb.IOComparison) string { return count(r.Glitches) }, key: func(r *pb.IOComparison) any { return r.Glitches }, extra: true},
}

func ratio(v, base float64) float64 {
	if base == 0 {
		return 0
	}
	return v / base
}

var sasErrorCols = []col[*pb.SasErrorRow]{
	{name: "host", header: "HOST", value: func(r *pb.SasErrorRow) string { return r.Hostname }},
	{name: "node", header: "NODE", value: func(r *pb.SasErrorRow) string { return orDash(r.OwnerName) }},
	{name: "phy", header: "PHY", value: func(r *pb.SasErrorRow) string { return fmt.Sprint(r.PhyId) }, key: func(r *pb.SasErrorRow) any { return r.PhyId }},
	{name: "port", header: "PORT", value: func(r *pb.SasErrorRow) string { return orDash(r.Port) }},
	{name: "attached", header: "ATTACHED", value: func(r *pb.SasErrorRow) string {
		switch {
		case r.DevName != "":
			s := r.DevName + " " + r.Serial
			if r.Bay != "" {
				s += " bay " + r.Bay
			}
			return s
		case r.AttachedKind == "upstream":
			return "upstream"
		}
		return orDash(r.Attached)
	}},
	{name: "invalid", header: "INVALID", value: func(r *pb.SasErrorRow) string { return fmt.Sprint(r.InvalidDword) }, key: func(r *pb.SasErrorRow) any { return r.InvalidDword }},
	{name: "disparity", header: "DISPARITY", value: func(r *pb.SasErrorRow) string { return fmt.Sprint(r.DisparityError) }, key: func(r *pb.SasErrorRow) any { return r.DisparityError }},
	{name: "dwsync", header: "DWSYNC", value: func(r *pb.SasErrorRow) string { return fmt.Sprint(r.LossDwordSync) }, key: func(r *pb.SasErrorRow) any { return r.LossDwordSync }},
	{name: "reset", header: "RESET", value: func(r *pb.SasErrorRow) string { return fmt.Sprint(r.PhyResetProblem) }, key: func(r *pb.SasErrorRow) any { return r.PhyResetProblem }},
	{name: "reports", header: "REPORTS", value: func(r *pb.SasErrorRow) string { return fmt.Sprint(r.Samples) }, key: func(r *pb.SasErrorRow) any { return r.Samples }},
	{name: "last", header: "LAST", value: func(r *pb.SasErrorRow) string { return when(r.LastAt) }, key: func(r *pb.SasErrorRow) any { return tsKey(r.LastAt) }},
	{name: "address", header: "ADDRESS", value: func(r *pb.SasErrorRow) string { return r.OwnerAddress }, extra: true},
	{name: "serial", header: "SERIAL", value: func(r *pb.SasErrorRow) string { return orDash(r.Serial) }, extra: true},
	{name: "device", header: "DEVICE", value: func(r *pb.SasErrorRow) string { return orDash(r.DevName) }, extra: true},
}

var profileCols = []col[*hardware.Profile]{
	{name: "model", header: "MODEL", value: func(p *hardware.Profile) string { return p.Match.Model }},
	{name: "board", header: "BOARD", value: func(p *hardware.Profile) string { return orDash(p.Match.Board) }},
	{name: "bays", header: "BAYS", value: func(p *hardware.Profile) string { return fmt.Sprint(len(p.Bays)) }, key: func(p *hardware.Profile) any { return len(p.Bays) }},
	{name: "layout", header: "LAYOUT", value: func(p *hardware.Profile) string {
		if p.Layout == nil {
			return "-"
		}
		return fmt.Sprintf("%dx%d", p.Layout.Rows, p.Layout.Columns)
	}},
	{name: "title", header: "TITLE", value: func(p *hardware.Profile) string { return p.Title }},
	{name: "file", header: "FILE", value: func(p *hardware.Profile) string { return p.File }},
	{name: "notes", header: "NOTES", value: func(p *hardware.Profile) string { return strings.ReplaceAll(p.Notes, "\n", " ") }, extra: true},
}
