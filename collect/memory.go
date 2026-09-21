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

	EDAC      string // the kernel's location, as the log prints it: "mc0/csrow2/ch2"; "" when EDAC has no entry for it
	EDACType  string // EDAC's own idea of the module: "Registered-DDR4"
	EDACBytes uint64 // what the EDAC entries matched to it add up to; a mismatch with SizeBytes means a wrong match
	Mapping   string // how Slot and EDAC were joined: "exact", "inferred", or "" when one side is missing
	CE, UE    uint64 // errors EDAC counted since boot
}

// MemoryInventory is every DIMM on the host, slots first in slot order,
// then EDAC entries that matched no slot, and the kernel's own total to
// check them against.
type MemoryInventory struct {
	DIMMs       []DIMM
	KernelBytes uint64 // MemTotal from /proc/meminfo; 0 when unreadable
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
	return &MemoryInventory{DIMMs: joinDIMMs(slots, edac), KernelBytes: readMemTotal(c.proc())}, nil
}

// readMemTotal is MemTotal from /proc/meminfo, in bytes; 0 when unreadable.
func readMemTotal(proc string) uint64 {
	b, err := os.ReadFile(filepath.Join(proc, "meminfo"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
			kb, _ := strconv.ParseUint(f[1], 10, 64)
			return kb << 10
		}
	}
	return 0
}

// smbiosMemory is one type 17 record with a module in it.
type smbiosMemory struct {
	DIMM
	socket     int    // from the bank locator ("P1_...", 0-based) or the locator ("P2-DIMMA1", "CPU1_DIMM_A1"); 0 when unsaid
	controller int    // from a locator that names one: "Controller1-ChannelA-DIMM0"; -1 otherwise
	letter     string // the channel letter the locator ends in or names: "C" of "DIMM_C2", "A" of "Controller0-ChannelA-DIMM0"
	number     int    // the slot number after the letter: 2 of "DIMM_C2"; the DIMM index of the Controller form
	channel    int    // from the bank locator when it names one; -1 otherwise
	index      int    // the DIMM index on that channel from the bank locator; -1 otherwise
}

var (
	// "DIMM_C2", "DIMMB1", "P1-DIMMC2", "CPU0_DIMM_A1", "DIMM C2"
	reLocator = regexp.MustCompile(`([A-Z])(\d+)\s*$`)
	// "Controller0-ChannelA-DIMM0" (Intel client boards)
	reController = regexp.MustCompile(`(?i)controller\s*(\d+).*channel\s*([A-Z]).*dimm\s*(\d+)`)
	// "P0_Node0_Channel1_Dimm1"
	reBank = regexp.MustCompile(`(?i)channel\s*(\d+)[_ ]*dimm\s*(\d+)`)
	// "P0 CHANNEL A": a bank locator that names the channel by letter,
	// for boards whose locators say only "DIMM 0" for every slot
	reBankLetter = regexp.MustCompile(`(?i)channel\s*([A-Z])\b`)
	// The socket: "P1_Node1_..." in a bank locator (0-based), "P2-DIMMA1"
	// or "CPU1_DIMM_A1" in a locator (Supermicro counts from 1 there).
	reBankSocket    = regexp.MustCompile(`^P(\d+)_`)
	reLocatorSocket = regexp.MustCompile(`^(?:P|CPU)(\d+)[_-]`)
)

// readSMBIOSMemory parses the type 17 records, keeping the ones with a
// module installed. They come from /sys/firmware/dmi/entries when the
// dmi-sysfs module is loaded, else from walking the raw table at
// /sys/firmware/dmi/tables/DMI, which every DMI-capable kernel exposes
// to root.
func readSMBIOSMemory(sys string) []smbiosMemory {
	var records [][]byte
	entries, _ := filepath.Glob(filepath.Join(sys, "firmware", "dmi", "entries", "17-*", "raw"))
	sort.Strings(entries)
	for _, path := range entries {
		if raw, err := os.ReadFile(path); err == nil {
			records = append(records, raw)
		}
	}
	if len(records) == 0 {
		if table, err := os.ReadFile(filepath.Join(sys, "firmware", "dmi", "tables", "DMI")); err == nil {
			records = smbiosRecords(table, 17)
		}
	}
	var out []smbiosMemory
	for _, raw := range records {
		if m, ok := parseSMBIOSMemory(raw); ok {
			out = append(out, m)
		}
	}
	// Some firmware calls every slot "DIMM 0" and tells them apart only
	// in the bank locator. The slot name is what modules are tracked by,
	// so a shared one gets the bank appended; failing that, its ordinal.
	names := map[string]int{}
	for _, m := range out {
		names[m.Slot]++
	}
	nth := map[string]int{}
	for i, m := range out {
		if names[m.Slot] < 2 {
			continue
		}
		nth[m.Slot]++
		switch {
		case m.Bank != "" && m.Bank != m.Slot:
			out[i].Slot = m.Slot + " (" + m.Bank + ")"
		default:
			out[i].Slot = fmt.Sprintf("%s #%d", m.Slot, nth[m.Slot])
		}
	}
	// Slot order, so listings read like the board.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].socket != out[j].socket {
			return out[i].socket < out[j].socket
		}
		if out[i].controller != out[j].controller {
			return out[i].controller < out[j].controller
		}
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

// smbiosRecords walks a raw SMBIOS table and returns every structure of
// the wanted type, formatted area and string table together, which is
// what a dmi-sysfs raw file holds. A structure is a 4-byte header (type,
// length, handle), the rest of the formatted area, then strings ended
// by a double NUL.
func smbiosRecords(table []byte, want byte) [][]byte {
	var out [][]byte
	for off := 0; off+4 <= len(table); {
		typ, length := table[off], int(table[off+1])
		if length < 4 || off+length > len(table) {
			break
		}
		end := off + length
		for end+1 < len(table) && !(table[end] == 0 && table[end+1] == 0) {
			end++
		}
		end += 2
		if end > len(table) {
			end = len(table)
		}
		if typ == want {
			out = append(out, table[off:end])
		}
		if typ == 127 { // end of table
			break
		}
		off = end
	}
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
	m := smbiosMemory{channel: -1, index: -1, controller: -1}
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
	m.controller = -1
	if cm := reController.FindStringSubmatch(m.Slot); cm != nil {
		m.controller, _ = strconv.Atoi(cm[1])
		m.letter = strings.ToUpper(cm[2])
		m.number, _ = strconv.Atoi(cm[3])
	} else if lm := reLocator.FindStringSubmatch(strings.ToUpper(m.Slot)); lm != nil {
		m.letter = lm[1]
		m.number, _ = strconv.Atoi(lm[2])
	}
	if bm := reBank.FindStringSubmatch(m.Bank); bm != nil {
		m.channel, _ = strconv.Atoi(bm[1])
		m.index, _ = strconv.Atoi(bm[2])
	} else if m.letter == "" {
		if bm := reBankLetter.FindStringSubmatch(m.Bank); bm != nil {
			m.letter = strings.ToUpper(bm[1])
		}
	}
	if sm := reBankSocket.FindStringSubmatch(m.Bank); sm != nil {
		m.socket, _ = strconv.Atoi(sm[1])
	} else if sm := reLocatorSocket.FindStringSubmatch(strings.ToUpper(m.Slot)); sm != nil {
		m.socket, _ = strconv.Atoi(sm[1])
		if strings.HasPrefix(strings.ToUpper(m.Slot), "P") && m.socket > 0 {
			m.socket-- // "P1-DIMMA1" is the first socket
		}
	}
	return m, true
}

// edacDIMM is one dimm (or rank) directory of an EDAC memory controller.
type edacDIMM struct {
	Location string // as the kernel log prints it: "mc0/csrow2/ch2"
	Label    string
	MemType  string
	SizeMB   uint64
	CE, UE   uint64
	mc       int
	socket   int // derived from the controller's place among the controllers; see globalChannels
	channel  int // -1 when the layout does not say; renumbered per socket by globalChannels
	rawChan  int // the channel as the driver numbers it within its controller
	index    int // the DIMM index on the channel: chip select / 2, or the slot; -1 when unknown
}

var reEDACLocation = regexp.MustCompile(`(csrow|channel|slot|memory|branch)\s+(\d+)`)

// readEDAC walks /sys/devices/system/edac/mc/mc*, keeping the dimm* and
// rank* entries (a chip-select based driver, AMD's, names them by rank)
// that have a size, which is how the driver marks a populated location.
func readEDAC(sys string) []edacDIMM {
	dirs, _ := filepath.Glob(filepath.Join(sys, "devices", "system", "edac", "mc", "mc*", "dimm*"))
	ranks, _ := filepath.Glob(filepath.Join(sys, "devices", "system", "edac", "mc", "mc*", "rank*"))
	dirs = append(dirs, ranks...)
	if len(dirs) == 0 {
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
		d.rawChan = d.channel
		// The kernel log names the entry by its label ("on <label>"), so
		// the label, rendered as the log follower renders it, is the key
		// the two meet on; the layout string only says where it sits.
		if d.Label != "" {
			d.Location = edacLocation(strconv.Itoa(d.mc), d.Label)
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].mc != out[j].mc {
			return out[i].mc < out[j].mc
		}
		if out[i].channel != out[j].channel {
			return out[i].channel < out[j].channel
		}
		return out[i].index < out[j].index
	})
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

// globalChannels renumbers EDAC channels the way the firmware counts
// them: per socket, across the socket's memory controllers in order. A
// Xeon with two controllers of two channels each has EDAC channels 0 and
// 1 on mc0 and mc1 but firmware channels 0 to 3; AMD has one controller
// per socket and the numbers already agree. The controllers per socket
// is the controller count over the socket count, and the channels per
// controller is what the firmware's channel count (from bank locators,
// else slot letters) divides into. A layout that does not divide evenly
// is left as it is.
func globalChannels(slots []smbiosMemory, edac []edacDIMM) {
	mcs := map[int]bool{}
	for _, d := range edac {
		mcs[d.mc] = true
	}
	sockets := map[int]bool{}
	channels := map[int]bool{}
	letters := map[string]bool{}
	for _, s := range slots {
		sockets[s.socket] = true
		if s.channel >= 0 {
			channels[s.channel] = true
		}
		if s.letter != "" && s.controller < 0 {
			letters[s.letter] = true
		}
	}
	nSockets := max(len(sockets), 1)
	if len(mcs) == 0 || len(mcs)%nSockets != 0 {
		return
	}
	perSocket := len(mcs) / nSockets
	sorted := make([]int, 0, len(mcs))
	for mc := range mcs {
		sorted = append(sorted, mc)
	}
	sort.Ints(sorted)
	ordinal := map[int]int{}
	for i, mc := range sorted {
		ordinal[mc] = i
	}
	fwChannels := max(len(channels), len(letters))
	perMC := 0
	if perSocket > 1 && fwChannels > 0 && fwChannels%perSocket == 0 {
		perMC = fwChannels / perSocket
	}
	for i := range edac {
		o := ordinal[edac[i].mc]
		edac[i].socket = o / perSocket
		if perMC > 0 && edac[i].channel >= 0 {
			edac[i].channel = (o%perSocket)*perMC + edac[i].channel
		}
	}
}

// joinDIMMs matches EDAC entries to firmware slots, by the first rule
// that applies to a slot:
//
//   - a bank locator naming channel and DIMM index ("P0_Node0_Channel1_Dimm1")
//     is exact;
//   - a locator naming the controller ("Controller0-ChannelA-DIMM0", Intel
//     client boards, where the driver splits a DDR5 module into its two
//     subchannels on one controller) takes every entry of that controller
//     when it holds one module, else the entry whose channel letter and
//     DIMM index match, exactly;
//   - the channel letter of the locator ("DIMM_C2") as the channel in
//     order, exact when that channel holds one module and a guess by slot
//     number when it holds several;
//   - failing all of those, when the unmatched slots and the unmatched
//     modules EDAC lists are equal in number, slot order to module order,
//     a guess.
//
// On a chip-select layout a module's ranks are separate EDAC entries, so
// a slot gathers every entry that points at it and sums their counts.
// Slots with no match keep their firmware description; EDAC entries with
// no match are listed by location alone.
func joinDIMMs(slots []smbiosMemory, edac []edacDIMM) []DIMM {
	globalChannels(slots, edac)
	byLetter := map[[2]any][]int{} // socket, letter -> slots
	byController := map[int][]int{}
	for i, s := range slots {
		if s.controller >= 0 {
			byController[s.controller] = append(byController[s.controller], i)
		} else if s.letter != "" {
			k := [2]any{s.socket, s.letter}
			byLetter[k] = append(byLetter[k], i)
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
		// Exact: the bank locator names this socket, channel and index.
		for i, s := range slots {
			if s.channel >= 0 && s.socket == d.socket && s.channel == d.channel && s.index == d.index {
				take(i, e, "exact")
				break
			}
		}
		if used[e] {
			continue
		}
		// A controller-naming locator: mc N is controller N.
		if slotsOn := byController[d.mc]; len(slotsOn) > 0 {
			if len(slotsOn) == 1 {
				take(slotsOn[0], e, "exact")
				continue
			}
			for _, i := range slotsOn {
				if d.rawChan >= 0 && slots[i].letter == string(rune('A'+d.rawChan)) && slots[i].number == d.index {
					take(i, e, "exact")
					break
				}
			}
			if used[e] {
				continue
			}
		}
		if d.channel < 0 {
			continue
		}
		candidates := byLetter[[2]any{d.socket, string(rune('A' + d.channel))}]
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
	// Positional fallback, only for slots that say nothing about where
	// they are ("PROC 1 DIMM 3"): group what is left by module and pair
	// in order. A slot that names a channel and still matched nothing
	// stays unmatched, since a wrong answer is worse than none.
	var freeSlots []int
	blind := true
	for i, s := range slots {
		if len(matches[i]) == 0 {
			freeSlots = append(freeSlots, i)
			if s.letter != "" || s.channel >= 0 || s.controller >= 0 {
				blind = false
			}
		}
	}
	if len(freeSlots) > 0 && blind {
		type moduleKey struct{ mc, channel, index int }
		var order []moduleKey
		groups := map[moduleKey][]int{}
		for e, d := range edac {
			if used[e] {
				continue
			}
			k := moduleKey{d.mc, d.channel, d.index}
			if _, ok := groups[k]; !ok {
				order = append(order, k)
			}
			groups[k] = append(groups[k], e)
		}
		if len(order) == len(freeSlots) {
			for n, k := range order {
				for _, e := range groups[k] {
					take(freeSlots[n], e, "inferred")
				}
			}
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
			d.EDACBytes += edac[e].SizeMB << 20
			d.EDACType = edac[e].MemType
		}
		d.EDAC, d.Mapping = strings.Join(locs, "+"), how[i]
		out = append(out, d)
	}
	for e, d := range edac {
		if used[e] {
			continue
		}
		out = append(out, DIMM{EDAC: d.Location, EDACType: d.MemType, EDACBytes: d.SizeMB << 20, CE: d.CE, UE: d.UE})
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
		ranks, _ := filepath.Glob(filepath.Join(mc, "rank*"))
		for _, d := range append(dimms, ranks...) {
			for _, name := range []string{"dimm_label", "dimm_location", "dimm_mem_type", "size", "dimm_ce_count", "dimm_ue_count"} {
				if err := c.copySys(dir, strings.TrimPrefix(filepath.Join(d, name), c.sys())); err != nil {
					slog.Debug("capture: edac", "path", d, "attr", name, "err", err)
				}
			}
		}
	}
	return nil
}
