package collect

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// type17 builds a type 17 record: a 0x28-byte formatted area (SMBIOS 3.2)
// and the string table. sizeMB 0 means no module.
func type17(locator, bank string, sizeMB uint32, memType byte, speed uint16, manufacturer, serial, part string, ranks byte) []byte {
	const length = 0x28
	raw := make([]byte, length)
	raw[0], raw[1] = 17, length
	if sizeMB == 0 {
		binary.LittleEndian.PutUint16(raw[0x0c:], 0)
	} else if sizeMB < 0x7fff {
		binary.LittleEndian.PutUint16(raw[0x0c:], uint16(sizeMB))
	} else {
		binary.LittleEndian.PutUint16(raw[0x0c:], 0x7fff)
		binary.LittleEndian.PutUint32(raw[0x1c:], sizeMB)
	}
	raw[0x10], raw[0x11] = 1, 2
	raw[0x12] = memType
	binary.LittleEndian.PutUint16(raw[0x15:], speed)
	raw[0x17], raw[0x18], raw[0x1a] = 3, 4, 5
	raw[0x1b] = ranks
	binary.LittleEndian.PutUint16(raw[0x20:], speed)
	for _, s := range []string{locator, bank, manufacturer, serial, part} {
		raw = append(raw, []byte(s)...)
		raw = append(raw, 0)
	}
	return append(raw, 0)
}

func writeMemoryFixture(t *testing.T, sys string, records [][]byte, edac map[string]map[string]string) {
	t.Helper()
	for i, r := range records {
		dir := filepath.Join(sys, "firmware", "dmi", "entries", "17-"+itoa(i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "raw"), r, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for dimm, attrs := range edac {
		dir := filepath.Join(sys, "devices", "system", "edac", "mc", dimm)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, v := range attrs {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(v+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func itoa(i int) string { return string(rune('0' + i)) }

// TestMemoryPBS1: a Supermicro H13SAE-MF, two DDR5 modules in DIMMA1 and
// DIMMB1, which the bank locator calls Channel0_Dimm1 and Channel1_Dimm1:
// the chip selects 2 and 3 of channel 1 are DIMMB1, exactly, however the
// slot is numbered. AMD's driver names its entries rank*, not dimm*.
func TestMemoryPBS1(t *testing.T) {
	sys := t.TempDir()
	writeMemoryFixture(t, sys, [][]byte{
		type17("DIMMA2", "P0_Node0_Channel0_Dimm0", 0, 0x22, 0, "", "", "", 0),
		type17("DIMMA1", "P0_Node0_Channel0_Dimm1", 32768, 0x22, 5600, "Micron Technology", "802C042537BB080000", "MB32G56U80M2R8.RtR", 2),
		type17("DIMMB2", "P0_Node0_Channel1_Dimm0", 0, 0x22, 0, "", "", "", 0),
		type17("DIMMB1", "P0_Node0_Channel1_Dimm1", 32768, 0x22, 5600, "Micron Technology", "80CE042542F50C0000", "MB32G56U80S2R8.RtR", 2),
	}, map[string]map[string]string{
		"mc0/rank2": {"dimm_label": "mc#0csrow#2channel#0", "dimm_location": "csrow 2 channel 0 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "16384", "dimm_ce_count": "0", "dimm_ue_count": "0"},
		"mc0/rank3": {"dimm_label": "mc#0csrow#3channel#0", "dimm_location": "csrow 3 channel 0 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "16384", "dimm_ce_count": "0", "dimm_ue_count": "0"},
		"mc0/rank6": {"dimm_label": "mc#0csrow#2channel#1", "dimm_location": "csrow 2 channel 1 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "16384", "dimm_ce_count": "4", "dimm_ue_count": "0"},
		"mc0/rank7": {"dimm_label": "mc#0csrow#3channel#1", "dimm_location": "csrow 3 channel 1 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "16384", "dimm_ce_count": "9083", "dimm_ue_count": "0"},
	})
	inv, err := (&Collector{Platform: "linux", Sys: sys}).Memory()
	if err != nil {
		t.Fatal(err)
	}
	// A rank is an EDAC entry, so a two-rank module gathers two; the join
	// keeps one line per firmware slot and sums its ranks.
	if len(inv.DIMMs) != 2 {
		t.Fatalf("DIMMs = %+v", inv.DIMMs)
	}
	a, b := inv.DIMMs[0], inv.DIMMs[1]
	if a.Slot != "DIMMA1" || a.SizeBytes != 32<<30 || a.Ranks != 2 || a.Type != "DDR5" || a.SpeedMTs != 5600 || a.Serial != "802C042537BB080000" || a.Part != "MB32G56U80M2R8.RtR" {
		t.Errorf("A1 = %+v", a)
	}
	if a.EDAC != "mc0/csrow2/ch0+mc0/csrow3/ch0" || a.Mapping != "exact" || a.CE != 0 {
		t.Errorf("A1 EDAC = %q %q ce=%d", a.EDAC, a.Mapping, a.CE)
	}
	if b.Slot != "DIMMB1" || b.EDAC != "mc0/csrow2/ch1+mc0/csrow3/ch1" || b.Mapping != "exact" || b.CE != 9087 || b.UE != 0 || b.EDACType != "Unbuffered-DDR5" {
		t.Errorf("B1 = %+v", b)
	}
}

// TestMemoryMGMT1: an ASUS RS500A-E10 whose bank locator says only
// "NODE 1"; eight DDR4 modules, one per channel, in the "2" slots. The
// letter of the locator is the channel, and with one module on it the
// match is exact whatever the chip select.
func TestMemoryMGMT1(t *testing.T) {
	sys := t.TempDir()
	var records [][]byte
	edac := map[string]map[string]string{}
	for ch := 0; ch < 8; ch++ {
		letter := string(rune('A' + ch))
		records = append(records, type17("DIMM_"+letter+"1", "NODE 1", 0, 0x1a, 0, "", "", "", 0))
		records = append(records, type17("DIMM_"+letter+"2", "NODE 1", 32768, 0x1a, 2400, "Samsung", "16E0"+letter, "M393A4K40BB1-CRC", 2))
		for cs := 2; cs < 4; cs++ {
			ce := "0"
			if ch == 2 && cs == 2 {
				ce = "2"
			}
			edac["mc0/dimm"+itoa(ch*4+cs)] = map[string]string{"dimm_location": "csrow " + itoa(cs) + " channel " + itoa(ch) + " ", "dimm_mem_type": "Registered-DDR4", "size": "16384", "dimm_ce_count": ce, "dimm_ue_count": "0"}
		}
	}
	writeMemoryFixture(t, sys, records, edac)
	inv, err := (&Collector{Platform: "linux", Sys: sys}).Memory()
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.DIMMs) != 8 {
		t.Fatalf("DIMMs = %d: %+v", len(inv.DIMMs), inv.DIMMs)
	}
	for i, d := range inv.DIMMs {
		want := "DIMM_" + string(rune('A'+i)) + "2"
		if d.Slot != want || d.Mapping != "exact" || d.EDAC == "" {
			t.Errorf("DIMM %d = %+v, want slot %s exact", i, d, want)
		}
	}
	if c := inv.DIMMs[2]; c.Slot != "DIMM_C2" || c.CE != 2 || c.EDAC != "mc0/csrow2/ch2+mc0/csrow3/ch2" {
		t.Errorf("C2 = %+v", c)
	}
}

// TestMemoryTwoPerChannel: no usable bank locator and two modules on a
// channel: the chip-select index picks the slot by number order, and the
// match is marked inferred. A slot EDAC never mentions, and an EDAC entry
// no slot letter covers, are both still listed.
func TestMemoryTwoPerChannel(t *testing.T) {
	sys := t.TempDir()
	writeMemoryFixture(t, sys, [][]byte{
		type17("DIMM_A1", "BANK 0", 16384, 0x1a, 3200, "Kingston", "A1S", "KSM32", 1),
		type17("DIMM_A2", "BANK 1", 16384, 0x1a, 3200, "Kingston", "A2S", "KSM32", 1),
		type17("DIMM_B1", "BANK 2", 16384, 0x1a, 3200, "Kingston", "B1S", "KSM32", 1),
	}, map[string]map[string]string{
		"mc0/dimm0": {"dimm_location": "csrow 0 channel 0 ", "dimm_mem_type": "Unbuffered-DDR4", "size": "16384", "dimm_ce_count": "1", "dimm_ue_count": "0"},
		"mc0/dimm2": {"dimm_location": "csrow 2 channel 0 ", "dimm_mem_type": "Unbuffered-DDR4", "size": "16384", "dimm_ce_count": "0", "dimm_ue_count": "0"},
		"mc0/dimm9": {"dimm_location": "csrow 1 channel 4 ", "dimm_mem_type": "Unbuffered-DDR4", "size": "16384", "dimm_ce_count": "0", "dimm_ue_count": "1"},
	})
	inv, _ := (&Collector{Platform: "linux", Sys: sys}).Memory()
	if len(inv.DIMMs) != 4 {
		t.Fatalf("DIMMs = %+v", inv.DIMMs)
	}
	if a1 := inv.DIMMs[0]; a1.Slot != "DIMM_A1" || a1.EDAC != "mc0/csrow0/ch0" || a1.Mapping != "inferred" || a1.CE != 1 {
		t.Errorf("A1 = %+v", a1)
	}
	if a2 := inv.DIMMs[1]; a2.Slot != "DIMM_A2" || a2.EDAC != "mc0/csrow2/ch0" || a2.Mapping != "inferred" {
		t.Errorf("A2 = %+v", a2)
	}
	if b1 := inv.DIMMs[2]; b1.Slot != "DIMM_B1" || b1.EDAC != "" || b1.Mapping != "" {
		t.Errorf("B1 = %+v", b1)
	}
	if e := inv.DIMMs[3]; e.Slot != "" || e.EDAC != "mc0/csrow1/ch4" || e.UE != 1 || e.SizeBytes != 16<<30 {
		t.Errorf("orphan EDAC entry = %+v", e)
	}
}

func TestMemoryNone(t *testing.T) {
	inv, err := (&Collector{Platform: "linux", Sys: t.TempDir()}).Memory()
	if err != nil || len(inv.DIMMs) != 0 {
		t.Errorf("Memory() on a bare tree = %+v, %v", inv, err)
	}
	if inv, _ := (&Collector{Platform: "darwin"}).Memory(); len(inv.DIMMs) != 0 {
		t.Errorf("Memory() on darwin = %+v", inv)
	}
}

// TestMemoryFS2: a Xeon with two memory controllers of two channels each.
// EDAC has channels 0 and 1 on mc0 and on mc1; the firmware counts 0 to 3
// across the socket. Channel order across controllers lines them up, and
// the location is the label the kernel log names the entry by.
func TestMemoryFS2(t *testing.T) {
	sys := t.TempDir()
	var records [][]byte
	edac := map[string]map[string]string{}
	for ch := 0; ch < 4; ch++ {
		letter := string(rune('A' + ch))
		for slot := 0; slot < 2; slot++ {
			records = append(records, type17("DIMM"+letter+itoa(slot+1), "P0_Node0_Channel"+itoa(ch)+"_Dimm"+itoa(slot), 32768, 0x1a, 2133, "Samsung", "18BE"+letter+itoa(slot), "M393A4K40BB1-CRC", 2))
			mc, mcCh := ch/2, ch%2
			label := "CPU_SrcID#0_Ha#" + itoa(mc) + "_Chan#" + itoa(mcCh) + "_DIMM#" + itoa(slot)
			ce := "0"
			if ch == 3 && slot == 1 {
				ce = "5"
			}
			edac["mc"+itoa(mc)+"/dimm"+itoa(mcCh*2+slot)] = map[string]string{"dimm_label": label, "dimm_location": "channel " + itoa(mcCh) + " slot " + itoa(slot) + " ", "dimm_mem_type": "Registered-DDR4", "size": "32768", "dimm_ce_count": ce, "dimm_ue_count": "0"}
		}
	}
	writeMemoryFixture(t, sys, records, edac)
	inv, _ := (&Collector{Platform: "linux", Sys: sys}).Memory()
	if len(inv.DIMMs) != 8 {
		t.Fatalf("DIMMs = %d: %+v", len(inv.DIMMs), inv.DIMMs)
	}
	for _, d := range inv.DIMMs {
		if d.Mapping != "exact" || d.EDAC == "" {
			t.Errorf("%s = %+v, want an exact match", d.Slot, d)
		}
	}
	if c1 := inv.DIMMs[4]; c1.Slot != "DIMMC1" || c1.EDAC != "mc1/CPU_SrcID#0_Ha#1_Chan#0_DIMM#0" {
		t.Errorf("C1 = %+v", c1)
	}
	if d2 := inv.DIMMs[7]; d2.Slot != "DIMMD2" || d2.EDAC != "mc1/CPU_SrcID#0_Ha#1_Chan#1_DIMM#1" || d2.CE != 5 {
		t.Errorf("D2 = %+v", d2)
	}
	// The log follower renders the same line the same way.
	c := newClassifier()
	ev, ok := c.classify(time.Time{}, "EDAC MC1: 1 CE memory read error on CPU_SrcID#0_Ha#1_Chan#1_DIMM#1 (channel:1 slot:1 page:0x1a2b3c offset:0x0 grain:32 syndrome:0x0)")
	if !ok || ev.Code() != "mc1/CPU_SrcID#0_Ha#1_Chan#1_DIMM#1" {
		t.Errorf("log line code = %q, %v", ev.Code(), ok)
	}
}

// TestMemoryDesk1: an MS-01, where the Intel client driver splits each
// DDR5 module into two 8 GB subchannels on its own controller and the
// firmware names the slots by controller. All of a controller's entries
// are its one module.
func TestMemoryDesk1(t *testing.T) {
	sys := t.TempDir()
	writeMemoryFixture(t, sys, [][]byte{
		type17("Controller0-ChannelA-DIMM0", "BANK 0", 16384, 0x22, 5200, "Crucial Technology", "E8F495F7", "CT16G56C46S5.M8G1", 1),
		type17("Controller1-ChannelA-DIMM0", "BANK 0", 16384, 0x22, 5200, "Crucial Technology", "E9626CFC", "CT16G56C46S5.M8D1", 1),
	}, map[string]map[string]string{
		"mc0/dimm0": {"dimm_location": "channel 0 slot 0 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "8192", "dimm_ce_count": "0", "dimm_ue_count": "0"},
		"mc0/dimm1": {"dimm_location": "channel 1 slot 0 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "8192", "dimm_ce_count": "0", "dimm_ue_count": "0"},
		"mc1/dimm0": {"dimm_location": "channel 0 slot 0 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "8192", "dimm_ce_count": "3", "dimm_ue_count": "0"},
		"mc1/dimm1": {"dimm_location": "channel 1 slot 0 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "8192", "dimm_ce_count": "0", "dimm_ue_count": "0"},
	})
	inv, _ := (&Collector{Platform: "linux", Sys: sys}).Memory()
	if len(inv.DIMMs) != 2 {
		t.Fatalf("DIMMs = %+v", inv.DIMMs)
	}
	if a := inv.DIMMs[0]; a.Slot != "Controller0-ChannelA-DIMM0" || a.EDAC != "mc0/ch0/slot0+mc0/ch1/slot0" || a.Mapping != "exact" || a.CE != 0 {
		t.Errorf("controller 0 = %+v", a)
	}
	if b := inv.DIMMs[1]; b.Slot != "Controller1-ChannelA-DIMM0" || b.EDAC != "mc1/ch0/slot0+mc1/ch1/slot0" || b.Mapping != "exact" || b.CE != 3 {
		t.Errorf("controller 1 = %+v", b)
	}
}

// TestMemoryD1: an HPE cartridge naming slots "PROC 1 DIMM 1" to "4" with
// nothing else to go on: four slots, four modules, paired in order and
// marked inferred.
func TestMemoryD1(t *testing.T) {
	sys := t.TempDir()
	var records [][]byte
	for i := 1; i <= 4; i++ {
		records = append(records, type17("PROC 1 DIMM "+itoa(i), "", 32768, 0x1a, 2133, "UNKNOWN", "", "NOT AVAILABLE", 2))
	}
	writeMemoryFixture(t, sys, records, map[string]map[string]string{
		"mc0/dimm0": {"dimm_location": "channel 0 slot 0 ", "dimm_mem_type": "Registered-DDR4", "size": "32768", "dimm_ce_count": "0", "dimm_ue_count": "0"},
		"mc0/dimm1": {"dimm_location": "channel 0 slot 1 ", "dimm_mem_type": "Registered-DDR4", "size": "32768", "dimm_ce_count": "0", "dimm_ue_count": "0"},
		"mc0/dimm2": {"dimm_location": "channel 1 slot 0 ", "dimm_mem_type": "Registered-DDR4", "size": "32768", "dimm_ce_count": "7", "dimm_ue_count": "0"},
		"mc0/dimm3": {"dimm_location": "channel 1 slot 1 ", "dimm_mem_type": "Registered-DDR4", "size": "32768", "dimm_ce_count": "0", "dimm_ue_count": "0"},
	})
	inv, _ := (&Collector{Platform: "linux", Sys: sys}).Memory()
	if len(inv.DIMMs) != 4 {
		t.Fatalf("DIMMs = %+v", inv.DIMMs)
	}
	for i, d := range inv.DIMMs {
		if d.Slot != "PROC 1 DIMM "+itoa(i+1) || d.Mapping != "inferred" || d.EDAC == "" {
			t.Errorf("DIMM %d = %+v", i, d)
		}
	}
	if d3 := inv.DIMMs[2]; d3.EDAC != "mc0/ch1/slot0" || d3.CE != 7 {
		t.Errorf("DIMM 3 = %+v", d3)
	}
}

// TestMemoryRawTable: no dmi-sysfs entries, so the records come from the
// raw table, walked structure by structure.
func TestMemoryRawTable(t *testing.T) {
	sys := t.TempDir()
	dir := filepath.Join(sys, "firmware", "dmi", "tables")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var table []byte
	// A type 16 structure ahead of it, with two strings, to walk past.
	table = append(table, 16, 0x0f, 0x10, 0x00, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)
	table = append(table, []byte("one\x00two\x00\x00")...)
	table = append(table, type17("DIMM_C2", "NODE 1", 32768, 0x1a, 2400, "Samsung", "32F52599", "M393A4K40BB1-CRC", 2)...)
	table = append(table, type17("DIMM_D1", "NODE 1", 0, 0x1a, 0, "", "", "", 0)...)
	table = append(table, 127, 4, 0, 0, 0, 0)
	if err := os.WriteFile(filepath.Join(dir, "DMI"), table, 0o600); err != nil {
		t.Fatal(err)
	}
	inv, _ := (&Collector{Platform: "linux", Sys: sys}).Memory()
	if len(inv.DIMMs) != 1 || inv.DIMMs[0].Slot != "DIMM_C2" || inv.DIMMs[0].Serial != "32F52599" || inv.DIMMs[0].SizeBytes != 32<<30 {
		t.Errorf("DIMMs from the raw table = %+v", inv.DIMMs)
	}
}

// TestMemoryMon1: an MS-A2 whose firmware calls both SODIMM slots "DIMM 0"
// and separates them only by bank ("P0 CHANNEL A" / "P0 CHANNEL B"), with
// no serial numbers. Both modules survive, named by their bank, and the
// bank's letter joins them to EDAC's channels.
func TestMemoryMon1(t *testing.T) {
	sys := t.TempDir()
	writeMemoryFixture(t, sys, [][]byte{
		type17("DIMM 0", "P0 CHANNEL A", 49152, 0x22, 5200, "Unknown", "00000000", "CMSX96GX5M2A5600C48", 2),
		type17("DIMM 0", "P0 CHANNEL B", 49152, 0x22, 5200, "Unknown", "00000000", "CMSX96GX5M2A5600C48", 2),
	}, map[string]map[string]string{
		"mc0/rank0": {"dimm_label": "mc#0csrow#0channel#0", "dimm_location": "csrow 0 channel 0 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "24576", "dimm_ce_count": "0", "dimm_ue_count": "0"},
		"mc0/rank1": {"dimm_label": "mc#0csrow#1channel#0", "dimm_location": "csrow 1 channel 0 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "24576", "dimm_ce_count": "0", "dimm_ue_count": "0"},
		"mc0/rank4": {"dimm_label": "mc#0csrow#0channel#1", "dimm_location": "csrow 0 channel 1 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "24576", "dimm_ce_count": "2", "dimm_ue_count": "0"},
		"mc0/rank5": {"dimm_label": "mc#0csrow#1channel#1", "dimm_location": "csrow 1 channel 1 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "24576", "dimm_ce_count": "0", "dimm_ue_count": "0"},
	})
	inv, _ := (&Collector{Platform: "linux", Sys: sys}).Memory()
	if len(inv.DIMMs) != 2 {
		t.Fatalf("DIMMs = %+v", inv.DIMMs)
	}
	if a := inv.DIMMs[0]; a.Slot != "DIMM 0 (P0 CHANNEL A)" || a.EDAC != "mc0/csrow0/ch0+mc0/csrow1/ch0" || a.Mapping != "exact" || a.SizeBytes != 48<<30 {
		t.Errorf("A = %+v", a)
	}
	if b := inv.DIMMs[1]; b.Slot != "DIMM 0 (P0 CHANNEL B)" || b.EDAC != "mc0/csrow0/ch1+mc0/csrow1/ch1" || b.Mapping != "exact" || b.CE != 2 {
		t.Errorf("B = %+v", b)
	}
	// Without banks either, the ordinal keeps them apart.
	sys2 := t.TempDir()
	writeMemoryFixture(t, sys2, [][]byte{
		type17("DIMM 0", "", 8192, 0x1a, 3200, "", "", "", 1),
		type17("DIMM 0", "", 8192, 0x1a, 3200, "", "", "", 1),
	}, nil)
	inv, _ = (&Collector{Platform: "linux", Sys: sys2}).Memory()
	if len(inv.DIMMs) != 2 || inv.DIMMs[0].Slot != "DIMM 0 #1" || inv.DIMMs[1].Slot != "DIMM 0 #2" {
		t.Errorf("nameless twins = %+v", inv.DIMMs)
	}
}
