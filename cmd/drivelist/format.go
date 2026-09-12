package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/scottlaird/drivelist"
	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
)

func when(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return "-"
	}
	return ts.AsTime().Local().Format("2006-01-02 15:04")
}

// ago renders how long before now a timestamp was, coarsely.
func ago(ts *timestamppb.Timestamp, now time.Time) string {
	if ts == nil {
		return "never"
	}
	d := now.Sub(ts.AsTime())
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func gap(secs float64) string {
	d := time.Duration(secs) * time.Second
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func slot(expander, bay string) string {
	switch {
	case expander == "" && bay == "":
		return "-"
	case bay == "":
		return expander
	}
	return expander + " bay " + bay
}

func size(b uint64) string {
	if b == 0 {
		return "-"
	}
	return drivelist.FormatDiskSize(b)
}

var busShort = map[pb.Bus]string{
	pb.Bus_BUS_SAS: "SAS", pb.Bus_BUS_SATA: "SATA", pb.Bus_BUS_NVME: "NVMe", pb.Bus_BUS_USB: "USB", pb.Bus_BUS_VIRTIO: "virtio",
}

// useSummary shortens use strings for a table: "zfs > space GUID > raidz2 GUID > disk GUID"
// becomes "zfs space/raidz2", "mount > /boot" stays.
func useSummary(uses []string) string {
	if len(uses) == 0 {
		return "-"
	}
	var out []string
	for _, u := range uses {
		parts := strings.Split(u, " > ")
		if parts[0] == "zfs" && len(parts) >= 2 {
			s := "zfs " + firstWord(parts[1])
			for _, p := range parts[2 : len(parts)-1] {
				s += "/" + firstWord(p)
			}
			if len(parts) == 2 {
				s += "/" + firstWord(parts[len(parts)-1])
			}
			out = append(out, s)
			continue
		}
		out = append(out, u)
	}
	return strings.Join(out, ", ")
}

func firstWord(s string) string {
	w, _, _ := strings.Cut(s, " ")
	return w
}

func health(m *pb.SmartSummary) string {
	if m == nil || m.Healthy == nil {
		return "-"
	}
	if *m.Healthy {
		return "ok"
	}
	return "FAILED"
}

func optU(p *uint64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprint(*p)
}

func optTemp(p *int32) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%d°C", *p)
}

func optBytes(p *uint64) string {
	if p == nil {
		return "-"
	}
	return drivelist.FormatDiskSize(*p)
}

func optPct(p *uint32) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%d%%", *p)
}

// detailOf decodes an event's detail JSON, tolerating anything.
func detailOf(e *pb.Event) map[string]any {
	m := map[string]any{}
	json.Unmarshal([]byte(e.GetDetail()), &m)
	return m
}

func str(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

func num(m map[string]any, k string) float64 {
	v, _ := m[k].(float64)
	return v
}

func usesOf(v any) []string {
	items, _ := v.([]any)
	var out []string
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// describe renders one event as a short line: what happened, where, and
// the one or two facts from the detail that matter for that kind.
func describe(e *pb.Event) string {
	d := detailOf(e)
	host := e.GetHostname()
	switch e.GetKind() {
	case "first_seen":
		return fmt.Sprintf("first seen    %s  %s  %s", host, slot(str(d, "expander"), str(d, "bay")), useSummary(usesOf(d["uses"])))
	case "appeared":
		return fmt.Sprintf("appeared      %s  %s  %s", host, slot(str(d, "expander"), str(d, "bay")), useSummary(usesOf(d["uses"])))
	case "vanished":
		s := fmt.Sprintf("vanished      %s  %s  last confirmed %s", host, slot(str(d, "expander"), str(d, "bay")), time.Unix(int64(num(d, "last_seen")), 0).Local().Format("2006-01-02 15:04"))
		if p := str(d, "still_in_pool"); p != "" {
			s += fmt.Sprintf("; pool %s still references it (%s)", p, str(d, "pool_state"))
		}
		return s
	case "reappeared":
		if same, _ := d["same_slot"].(bool); same {
			return fmt.Sprintf("reappeared    %s  %s  gap %s  %s", host, slot(str(d, "expander"), str(d, "bay")), gap(num(d, "gap_secs")), useSummary(usesOf(d["uses"])))
		}
		return fmt.Sprintf("moved         %s  %s  %s  (from %s %s, gap %s)", host, slot(str(d, "expander"), str(d, "bay")), useSummary(usesOf(d["uses"])), str(d, "from_host"), slot(str(d, "from_expander"), str(d, "from_bay")), gap(num(d, "gap_secs")))
	case "moved_host":
		return fmt.Sprintf("moved         %s  %s  %s  (from %s %s)", host, slot(str(d, "to_expander"), str(d, "to_bay")), useSummary(usesOf(d["to_uses"])), str(d, "from_host"), slot(str(d, "from_expander"), str(d, "from_bay")))
	case "moved_bay":
		return fmt.Sprintf("moved bay     %s  %s  (from %s)", host, slot(str(d, "to_expander"), str(d, "to_bay")), slot(str(d, "from_expander"), str(d, "from_bay")))
	case "use_changed":
		return fmt.Sprintf("use changed   %s  %s  %s  (was %s)", host, slot(str(d, "to_expander"), str(d, "to_bay")), useSummary(usesOf(d["to_uses"])), useSummary(usesOf(d["from_uses"])))
	case "member_state_changed":
		return fmt.Sprintf("zfs state     %s  %s -> %s", host, str(d, "from"), str(d, "to"))
	case "status_changed":
		return fmt.Sprintf("status        %s  %s: %q", str(d, "status"), strings.TrimPrefix(e.GetSource(), "user:"), str(d, "note"))
	case "note":
		return fmt.Sprintf("note          %s: %q", strings.TrimPrefix(e.GetSource(), "user:"), str(d, "note"))
	case "identity_conflict":
		return fmt.Sprintf("identity conflict  keys %v match drives %v", d["keys"], d["drives"])
	case "smart_warning":
		return fmt.Sprintf("smart         %s  %v  %s", host, d["reasons"], str(d, "dev_name"))
	case "kernel_warning":
		return fmt.Sprintf("kernel        %s  %s %s ×%v  %s", host, str(d, "class"), str(d, "code"), num(d, "count"), str(d, "dev_name"))
	case "pool_missing_member":
		return fmt.Sprintf("pool ghost    %s  pool %s expects %s (%s)", host, str(d, "pool"), str(d, "path"), str(d, "state"))
	case "host_first_seen":
		return fmt.Sprintf("host seen     %s", host)
	case "host_stale":
		return fmt.Sprintf("host stale    %s  silent %s", host, gap(num(d, "silent_secs")))
	case "host_resumed":
		return fmt.Sprintf("host resumed  %s  after %s", host, gap(num(d, "silent_secs")))
	case "report_degraded":
		return fmt.Sprintf("degraded      %s  unidentified %v", host, d["unidentified"])
	}
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, d[k]))
	}
	return fmt.Sprintf("%-13s %s  %s", e.GetKind(), host, strings.Join(parts, " "))
}
