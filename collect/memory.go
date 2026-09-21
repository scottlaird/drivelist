package collect

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DIMM is one memory module as the firmware describes it (SMBIOS type 17)
// joined to what the kernel's EDAC driver counts for it, when the two
// can be matched. A DIMM the firmware lists but EDAC does not carries no
// counts; an EDAC entry no slot could be matched to has no Slot.
type DIMM struct {
	Slot         string // the firmware's device locator: "DIMM_C2", "DIMMB1"
	Bank         string // the firmware's bank locator: "P0_Node0_Channel1_Dimm1", "NODE 1"
	SizeBytes    uint64
	Ranks        int
	Type         string // "DDR4", "DDR5"
	SpeedMTs     int    // configured speed, else rated
	Manufacturer string
	Part         string
	Serial       string

	EDAC     string // the kernel's location, as the log prints it: "mc0/csrow2/ch2"; "" when EDAC has no entry for it
	EDACType string // EDAC's own idea of the module: "Registered-DDR4"
	Mapping  string // how Slot and EDAC were joined: "exact", "inferred", or "" when one side is missing
	CE, UE   uint64 // errors EDAC counted since boot
}

// MemoryInventory is every DIMM on the host, slots first in slot order,
// then EDAC entries that matched no slot.
type MemoryInventory struct {
	DIMMs []DIMM
}

// Memory reads the memory modules from SMBIOS and their error counts
// from EDAC, and joins the two. Either source may be absent (no root, a
// board with no type 17 records, no EDAC driver); the result then has
// only what the other says. A host with neither returns an empty
// inventory and no error.
func (c *Collector) Memory() (*MemoryInventory, error) {
	if c.platform() != "linux" {
		return &MemoryInventory{}, nil
	}
	slots := readSMBIOSMemory(c.sys())
	edac := readEDAC(c.sys())
	return &MemoryInventory{DIMMs: joinDIMMs(slots, edac)}, nil
}

// smbiosMemory is one type 17 record with a module in it.
type smbiosMemory struct {
	DIMM
	letter  string // the channel letter the locator ends in: "C" of "DIMM_C2"
	number  int    // and its slot number: 2
	channel int    // from the bank locator when it names one; -1 otherwise
	index   int    // the DIMM index on that channel from the bank locator; -1 otherwise
}

var (
	// "DIMM_C2", "DIMMB1", "P1-DIMMC2", "CPU0_DIMM_A1", "DIMM C2"
	reLocator = regexp.MustCompile(`([A-Z])(\d+)\s*$`)
	// "P0_Node0_Channel1_Dimm1"
	reBank = regexp.MustCompile(`(?i)channel\s*(\d+)[_ ]*dimm\s*(\d+)`)
)

// readSMBIOSMemory parses the type 17 records under
// /sys/firmware/dmi/entries, keeping the ones with a module installed.
func readSMBIOSMemory(sys string) []smbiosMemory {
	entries, err := filepath.Glob(filepath.Join(sys, "firmware", "dmi", "entries", "17-*", "raw"))
	if err != nil || len(entries) == 0 {
		return nil
	}
	sort.Strings(entries)
	var out []smbiosMemory
	for _, path := range entries {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if m, ok := parseSMBIOSMemory(raw); ok {
			out = append(out, m)
		}
	}
	// Slot order, so listings read like the board.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].letter != out[j].letter {
			return out[i].letter < out[j].letter
		}
		if out[i].number != out[j].number {
			return out[i].number < out[j].number
		}
		return out[i].Slot < out[j].Slot
	})
	return out
}

// smbiosStrings is the string table after a structure's formatted area:
// NUL-terminated strings, ended by an empty one.
func smbiosStrings(raw []byte, length int) []string {
	var strs []string
	for rest := raw[length:]; len(rest) > 0; {
		i := 0
		for i < len(rest) && rest[i] != 0 {
			i++
		}
		if i == 0 {
			break
		}
		strs = append(strs, string(rest[:i]))
		if i+1 > len(rest) {
			break
		}
		rest = rest[i+1:]
	}
	return strs
}

var smbiosMemoryTypes = map[byte]string{
	0x12: "DDR", 0x13: "DDR2", 0x18: "DDR3", 0x1a: "DDR4", 0x1b: "LPDDR", 0x1c: "LPDDR2", 0x1d: "LPDDR3", 0x1e: "LPDDR4",
	0x1f: "Logical non-volatile", 0x20: "HBM", 0x21: "HBM2", 0x22: "DDR5", 0x23: "LPDDR5", 0x24: "HBM3",
}

// parseSMBIOSMemory decodes one type 17 (Memory Device) structure. Size is
// the word at 12 (0 means no module; bit 15 means kilobytes; 0x7FFF means
// the dword at 28 holds it in megabytes); locators are string indexes at
// 16 and 17; type is byte 18; speed the word at 21; manufacturer, serial
// and part strings at 23, 24 and 26; ranks the low nibble of byte 27;
// the configured speed the word at 32 (SMBIOS 2.7).
func parseSMBIOSMemory(raw []byte) (smbiosMemory, bool) {
	if len(raw) < 4 || raw[0] != 17 {
		return smbiosMemory{}, false
	}
	length := int(raw[1])
	if length > len(raw) || length < 0x15 {
		return smbiosMemory{}, false
	}
	size := binary.LittleEndian.Uint16(raw[0x0c:0x0e])
	if size == 0 || size == 0xffff {
		return smbiosMemory{}, false
	}
	m := smbiosMemory{channel: -1, index: -1}
	switch {
	case size == 0x7fff && length >= 0x20:
		m.SizeBytes = uint64(binary.LittleEndian.Uint32(raw[0x1c:0x20])) << 20
	case size&0x8000 != 0:
		m.SizeBytes = uint64(size&0x7fff) << 10
	default:
		m.SizeBytes = uint64(size) << 20
	}
	strs := smbiosStrings(raw, length)
	str := func(off int) string {
		if off >= length {
			return ""
		}
		if idx := int(raw[off]); idx > 0 && idx <= len(strs) {
			return strings.TrimSpace(strs[idx-1])
		}
		return ""
	}
	m.Slot, m.Bank = str(0x10), str(0x11)
	if length > 0x12 {
		m.Type = smbiosMemoryTypes[raw[0x12]]
		if m.Type == "" {
			m.Type = fmt.Sprintf("type 0x%02x", raw[0x12])
		}
	}
	if length >= 0x17 {
		m.SpeedMTs = int(binary.LittleEndian.Uint16(raw[0x15:0x17]))
	}
	m.Manufacturer, m.Serial, m.Part = str(0x17), str(0x18), str(0x1a)
	if length > 0x1b {
		m.Ranks = int(raw[0x1b] & 0x0f)
	}
	if length >= 0x22 {
		if v := int(binary.LittleEndian.Uint16(raw[0x20:0x22])); v > 0 {
			m.SpeedMTs = v
		}
	}
	if lm := reLocator.FindStringSubmatch(strings.ToUpper(m.Slot)); lm != nil {
		m.letter = lm[1]
		m.number, _ = strconv.Atoi(lm[2])
	}
	if bm := reBank.FindStringSubmatch(m.Bank); bm != nil {
		m.channel, _ = strconv.Atoi(bm[1])
		m.index, _ = strconv.Atoi(bm[2])
	}
	return m, true
}

// edacDIMM is one dimm directory of an EDAC memory controller.
type edacDIMM struct {
	Location string // as the kernel log prints it: "mc0/csrow2/ch2"
	Label    string
	MemType  string
	SizeMB   uint64
	CE, UE   uint64
	mc       int
	channel  int // -1 when the layout does not say
	index    int // the DIMM index on the channel: chip select / 2, or the slot; -1 when unknown
}

var reEDACLocation = regexp.MustCompile(`(csrow|channel|slot|memory|branch)\s+(\d+)`)

// readEDAC walks /sys/devices/system/edac/mc/mc*/dimm*, keeping entries
// that have a size, which is how the driver marks a populated location.
func readEDAC(sys string) []edacDIMM {
	dirs, err := filepath.Glob(filepath.Join(sys, "devices", "system", "edac", "mc", "mc*", "dimm*"))
	if err != nil || len(dirs) == 0 {
		return nil
	}
	sort.Strings(dirs)
	var out []edacDIMM
	for _, dir := range dirs {
		read := func(name string) string {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(b))
		}
		num := func(name string) uint64 {
			n, _ := strconv.ParseUint(read(name), 10, 64)
			return n
		}
		d := edacDIMM{Label: read("dimm_label"), MemType: read("dimm_mem_type"), SizeMB: num("size"), CE: num("dimm_ce_count"), UE: num("dimm_ue_count"), channel: -1, index: -1}
		if d.SizeMB == 0 {
			continue
		}
		mc := filepath.Base(filepath.Dir(dir))
		d.mc, _ = strconv.Atoi(strings.TrimPrefix(mc, "mc"))
		d.Location = edacLocationOf(d.mc, read("dimm_location"), &d)
		out = append(out, d)
	}
	return out
}

// edacLocationOf renders a dimm_location ("csrow 2 channel 2 ", or
// "channel 1 slot 0 ") the way edacLocation renders the log line, and
// fills in the channel and DIMM index the layout implies. On a chip-select
// layout each DIMM slot owns two chip selects, one per rank, so the
// index is the chip select halved.
func edacLocationOf(mc int, location string, d *edacDIMM) string {
	parts := []string{"mc" + strconv.Itoa(mc)}
	csrow := -1
	for _, m := range reEDACLocation.FindAllStringSubmatch(location, -1) {
		n, _ := strconv.Atoi(m[2])
		switch m[1] {
		case "csrow":
			csrow = n
			parts = append(parts, "csrow"+m[2])
		case "channel":
			d.channel = n
			parts = append(parts, "ch"+m[2])
		case "slot":
			d.index = n
			parts = append(parts, "slot"+m[2])
		default:
			parts = append(parts, m[1]+m[2])
		}
	}
	if csrow >= 0 && d.index < 0 {
		d.index = csrow / 2
	}
	return strings.Join(parts, "/")
}

// joinDIMMs matches EDAC entries to firmware slots. A bank locator that
// names the channel and DIMM index is exact. Failing that, the channel
// letter of the locator is taken to be the EDAC channel in order (A is
// 0), which is exact when that channel holds one module and a guess by
// slot number when it holds several. On a chip-select layout a module's
// ranks are separate EDAC entries, so a slot gathers every entry that
// points at it and sums their counts. Slots with no match keep their
// firmware description; EDAC entries with no match are listed by
// location alone.
func joinDIMMs(slots []smbiosMemory, edac []edacDIMM) []DIMM {
	byLetter := map[string][]int{}
	for i, s := range slots {
		if s.letter != "" {
			byLetter[s.letter] = append(byLetter[s.letter], i)
		}
	}
	matches := make([][]int, len(slots)) // slot -> EDAC indexes
	how := make([]string, len(slots))
	used := map[int]bool{}
	take := func(slot, e int, mapping string) {
		matches[slot] = append(matches[slot], e)
		if how[slot] == "" || mapping == "exact" {
			how[slot] = mapping
		}
		used[e] = true
	}
	for e, d := range edac {
		if d.channel < 0 {
			continue
		}
		exact := -1
		for i, s := range slots {
			if s.channel == d.channel && s.index == d.index {
				exact = i
				break
			}
		}
		if exact >= 0 {
			take(exact, e, "exact")
			continue
		}
		candidates := byLetter[string(rune('A'+d.channel))]
		var free []int
		for _, i := range candidates {
			if slots[i].channel < 0 {
				free = append(free, i)
			}
		}
		switch {
		case len(candidates) == 1 && len(free) == 1:
			take(free[0], e, "exact")
		case len(free) > 0 && d.index >= 0 && d.index < len(candidates):
			// Several modules on the channel: the index counts slots in
			// number order. A hardware profile is the place to correct it.
			take(candidates[d.index], e, "inferred")
		}
	}
	out := make([]DIMM, 0, len(slots)+len(edac))
	for i, s := range slots {
		d := s.DIMM
		var locs []string
		for _, e := range matches[i] {
			locs = append(locs, edac[e].Location)
			d.CE += edac[e].CE
			d.UE += edac[e].UE
			d.EDACType = edac[e].MemType
		}
		d.EDAC, d.Mapping = strings.Join(locs, "+"), how[i]
		out = append(out, d)
	}
	for e, d := range edac {
		if used[e] {
			continue
		}
		out = append(out, DIMM{EDAC: d.Location, EDACType: d.MemType, SizeBytes: d.SizeMB << 20, CE: d.CE, UE: d.UE})
	}
	return out
}

// captureMemory copies the type 17 records and the EDAC tree into a
// fixture.
func (c *Collector) captureMemory(dir string) error {
	entries, _ := filepath.Glob(filepath.Join(c.sys(), "firmware", "dmi", "entries", "17-*", "raw"))
	for _, path := range entries {
		if err := c.copySys(dir, strings.TrimPrefix(path, c.sys())); err != nil {
			slog.Debug("capture: dmi type 17", "path", path, "err", err) // needs root
		}
	}
	dirs, _ := filepath.Glob(filepath.Join(c.sys(), "devices", "system", "edac", "mc", "mc*"))
	for _, mc := range dirs {
		for _, name := range []string{"mc_name", "ce_count", "ue_count", "size_mb"} {
			_ = c.copySys(dir, strings.TrimPrefix(filepath.Join(mc, name), c.sys()))
		}
		dimms, _ := filepath.Glob(filepath.Join(mc, "dimm*"))
		for _, d := range dimms {
			for _, name := range []string{"dimm_label", "dimm_location", "dimm_mem_type", "size", "dimm_ce_count", "dimm_ue_count"} {
				if err := c.copySys(dir, strings.TrimPrefix(filepath.Join(d, name), c.sys())); err != nil {
					slog.Debug("capture: edac", "path", d, "attr", name, "err", err)
				}
			}
		}
	}
	return nil
}
