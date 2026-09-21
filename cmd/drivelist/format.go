package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
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

// sasWhere says what a phy leads to, for event lines: the drive and bay,
// the expander, or "upstream".
func sasWhere(d map[string]any) string {
	switch str(d, "attached_kind") {
	case "drive", "device":
		s := " (" + str(d, "attached")
		if dev := str(d, "dev_name"); dev != "" {
			s = " (" + dev
		}
		if bay := str(d, "bay"); bay != "" {
			s += " bay " + bay
		}
		return s + ")"
	case "expander":
		return " (" + str(d, "attached") + ")"
	case "upstream":
		return " (upstream)"
	}
	return ""
}

func sasDev(dev string) string {
	if dev == "" {
		return ""
	}
	return " " + dev
}

// sasGrowth renders the counters that grew: "+12 invalid dwords, +3 dword sync".
func sasGrowth(v any) string {
	m, _ := v.(map[string]any)
	var parts []string
	for _, k := range []struct{ key, label string }{{"invalid_dword", "invalid dwords"}, {"disparity_error", "disparity"}, {"loss_dword_sync", "dword sync"}, {"phy_reset_problem", "reset problems"}} {
		if n := num(m, k.key); n > 0 {
			parts = append(parts, fmt.Sprintf("+%d %s", int64(n), k.label))
		}
	}
	return strings.Join(parts, ", ")
}

// count renders a counter that is usually zero as "-" so the column
// stays quiet.
func count(n uint32) string {
	if n == 0 {
		return "-"
	}
	return strconv.FormatUint(uint64(n), 10)
}

// slotOf renders a placement's slot with the enclosure as a person would
// name it: the name they gave it, else the kernel's name for the node
// that reaches it, else the key.
func slotOf(p *pb.Placement) string {
	if p == nil {
		return "-"
	}
	return slot(firstOf(p.EnclosureName, p.EnclosureVia, p.Enclosure), firstOf(p.BayLabel, p.Bay))
}

// slotD renders the slot an event's detail describes under a key prefix
// ("", "from_", "to_"), preferring the person's name, then the kernel's.
// Events from before 0.7 carry expander keys instead.
func slotD(d map[string]any, prefix string) string {
	return slot(firstOf(str(d, prefix+"enclosure_name"), str(d, prefix+"enclosure_via"), str(d, prefix+"enclosure"),
		str(d, prefix+"expander_name"), str(d, prefix+"expander_dev"), str(d, prefix+"expander")), firstOf(str(d, prefix+"bay_label"), str(d, prefix+"bay")))
}

func firstOf(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
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

// eccText says whether a module carries check bits, by its widths:
// "72/64" for ECC DDR4, "80/64" for ECC DDR5, "none" for 64/64, "-"
// when the firmware did not say.
func eccText(d *pb.Dimm) string {
	switch {
	case d.DataWidth == 0:
		return "-"
	case d.TotalWidth > d.DataWidth:
		return fmt.Sprintf("%d/%d", d.TotalWidth, d.DataWidth)
	}
	return "none"
}

// memSize formats a memory module's size in binary units, which is how
// modules are sold: 34359738368 is "32 GB", not "34.4 GB".
func memSize(b uint64) string {
	switch {
	case b == 0:
		return "-"
	case b%(1<<30) == 0:
		return fmt.Sprintf("%d GB", b>>30)
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	}
	return fmt.Sprintf("%d MB", b>>20)
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

func fdiv(a, b uint64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func ms(v float64) string {
	if v == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1fms", v)
}

func pct(v float64) string {
	return fmt.Sprintf("%.0f%%", v*100)
}

// times renders v relative to a group median: "1.0×", "3.2×", or "-"
// when there is no median to compare with.
func times(v, median float64) string {
	if median == 0 {
		if v == 0 {
			return "1.0×"
		}
		return "-"
	}
	return fmt.Sprintf("%.1f×", v/median)
}

// groupLabel shortens a vdev group key: "zfs > space 5925… > raidz2 1237…"
// becomes "space/raidz2 …7295 (8)"; the empty group is "no pool".
func groupLabel(group string, size int) string {
	if group == "" {
		return fmt.Sprintf("no pool (%d)", size)
	}
	parts := strings.Split(group, " > ")
	if len(parts) < 2 {
		return group
	}
	label := firstWord(parts[1])
	for _, p := range parts[2:] {
		w, guid, _ := strings.Cut(p, " ")
		if len(guid) > 4 {
			guid = "…" + guid[len(guid)-4:]
		}
		label += "/" + w + " " + guid
	}
	return fmt.Sprintf("%s (%d)", label, size)
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
		return fmt.Sprintf("first seen    %s  %s  %s", host, slotD(d, ""), useSummary(usesOf(d["uses"])))
	case "appeared":
		return fmt.Sprintf("appeared      %s  %s  %s", host, slotD(d, ""), useSummary(usesOf(d["uses"])))
	case "vanished":
		s := fmt.Sprintf("vanished      %s  %s  last confirmed %s", host, slotD(d, ""), time.Unix(int64(num(d, "last_seen")), 0).Local().Format("2006-01-02 15:04"))
		if p := str(d, "still_in_pool"); p != "" {
			s += fmt.Sprintf("; pool %s still references it (%s)", p, str(d, "pool_state"))
		}
		return s
	case "reappeared":
		if same, _ := d["same_slot"].(bool); same {
			return fmt.Sprintf("reappeared    %s  %s  gap %s  %s", host, slotD(d, ""), gap(num(d, "gap_secs")), useSummary(usesOf(d["uses"])))
		}
		return fmt.Sprintf("moved         %s  %s  %s  (from %s %s, gap %s)", host, slotD(d, ""), useSummary(usesOf(d["uses"])), str(d, "from_host"), slotD(d, "from_"), gap(num(d, "gap_secs")))
	case "moved_host":
		return fmt.Sprintf("moved         %s  %s  %s  (from %s %s)", host, slotD(d, "to_"), useSummary(usesOf(d["to_uses"])), str(d, "from_host"), slotD(d, "from_"))
	case "moved_bay":
		return fmt.Sprintf("moved bay     %s  %s  (from %s)", host, slotD(d, "to_"), slotD(d, "from_"))
	case "use_changed":
		return fmt.Sprintf("use changed   %s  %s  %s  (was %s)", host, slotD(d, "to_"), useSummary(usesOf(d["to_uses"])), useSummary(usesOf(d["from_uses"])))
	case "enclosure_renamed", "expander_renamed":
		return fmt.Sprintf("enclosure     %s  %s is now %s (%v drives kept their bays)", host, firstOf(str(d, "from_name"), str(d, "from_via"), str(d, "from_dev"), str(d, "from")), firstOf(str(d, "to_name"), str(d, "to_via"), str(d, "to_dev"), str(d, "to")), d["drives"])
	case "sas_link_changed":
		return fmt.Sprintf("sas link      %s  %s phy %v%s  %s -> %s", host, str(d, "owner_name"), d["phy"], sasWhere(d), orDash(str(d, "from")), orDash(str(d, "to")))
	case "sas_attached_changed":
		return fmt.Sprintf("sas recabled  %s  %s phy %v  now %s%s (was %s %s)", host, str(d, "owner_name"), d["phy"], str(d, "to_attached"), sasDev(str(d, "to_dev_name")), str(d, "from_attached"), str(d, "from_dev_name"))
	case "sas_port_changed":
		return fmt.Sprintf("sas port      %s  %s %s -> %s  %v -> %v phys", host, str(d, "owner_name"), str(d, "port"), orDash(str(d, "attached")), d["from"], d["to"])
	case "sas_errors":
		return fmt.Sprintf("sas errors    %s  %s phy %v%s  %s", host, str(d, "owner_name"), d["phy"], sasWhere(d), sasGrowth(d["grew"]))
	case "sas_node_changed":
		switch str(d, "change") {
		case "revision":
			return fmt.Sprintf("sas node      %s  %s (%s) firmware %s -> %s", host, str(d, "name"), strings.TrimSpace(str(d, "product")), str(d, "from"), str(d, "to"))
		}
		return fmt.Sprintf("sas node      %s  %s (%s) %s", host, str(d, "name"), strings.TrimSpace(str(d, "product")), str(d, "change"))
	case "member_state_changed":
		return fmt.Sprintf("zfs state     %s  %s -> %s", host, str(d, "from"), str(d, "to"))
	case "status_changed":
		return fmt.Sprintf("status        %s  %s: %q", str(d, "status"), strings.TrimPrefix(e.GetSource(), "user:"), str(d, "note"))
	case "note":
		return fmt.Sprintf("note          %s: %q", strings.TrimPrefix(e.GetSource(), "user:"), str(d, "note"))
	case "identity_conflict":
		return fmt.Sprintf("identity conflict  keys %v match drives %v", d["keys"], d["drives"])
	case "merged":
		return fmt.Sprintf("merged        record %s (%s) folded into this drive by %s", str(d, "from_serial"), str(d, "from_wwn"), strings.TrimPrefix(e.GetSource(), "user:"))
	case "host_merged":
		return fmt.Sprintf("host merged   %s absorbed %s (%s) by %s", host, str(d, "from_hostname"), str(d, "from_machine_id"), strings.TrimPrefix(e.GetSource(), "user:"))
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
	case "host_rebooted":
		s := "host rebooted " + host
		if _, ok := d["up_secs"]; ok {
			s += "  up " + gap(num(d, "up_secs")) + " before"
		}
		if _, ok := d["silent_secs"]; ok {
			s += "  silent " + gap(num(d, "silent_secs"))
		}
		return s
	case "hardware_error":
		where := str(d, "code")
		if slot := str(d, "slot"); slot != "" {
			where = slot + " (" + str(d, "code") + ")"
		}
		return fmt.Sprintf("hardware      %s  %s %s ×%v  %s", host, str(d, "class"), where, num(d, "count"), str(d, "sample"))
	case "memory_errors":
		grew, _ := d["grew"].(map[string]any)
		total, _ := d["total"].(map[string]any)
		return fmt.Sprintf("memory errors %s  %s %s  +%v corrected, +%v uncorrected (%v/%v since boot)", host, str(d, "label"), str(d, "serial"), num(grew, "ce"), num(grew, "ue"), num(total, "ce"), num(total, "ue"))
	case "dimm_changed":
		label := str(d, "slot")
		if label == "" {
			label = str(d, "edac")
		}
		switch str(d, "change") {
		case "replaced":
			return fmt.Sprintf("dimm replaced %s  %s  %s %s (was %s %s)", host, label, str(d, "part"), str(d, "serial"), str(d, "from_part"), str(d, "from_serial"))
		default:
			return fmt.Sprintf("dimm %-8s %s  %s  %s %s", str(d, "change"), host, label, str(d, "part"), str(d, "serial"))
		}
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

// usageArgs validates a command's positional argument count and, when it
// is wrong, says how the command is used rather than how many arguments
// cobra counted. max is -1 for no upper bound.
func usageArgs(min, max int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < min || (max >= 0 && len(args) > max) {
			return fmt.Errorf("usage: %s", usage)
		}
		return nil
	}
}
