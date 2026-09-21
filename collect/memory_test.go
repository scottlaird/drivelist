package collect

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
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
// slot is numbered.
func TestMemoryPBS1(t *testing.T) {
	sys := t.TempDir()
	writeMemoryFixture(t, sys, [][]byte{
		type17("DIMMA2", "P0_Node0_Channel0_Dimm0", 0, 0x22, 0, "", "", "", 0),
		type17("DIMMA1", "P0_Node0_Channel0_Dimm1", 32768, 0x22, 5600, "Micron Technology", "802C042537BB080000", "MB32G56U80M2R8.RtR", 2),
		type17("DIMMB2", "P0_Node0_Channel1_Dimm0", 0, 0x22, 0, "", "", "", 0),
		type17("DIMMB1", "P0_Node0_Channel1_Dimm1", 32768, 0x22, 5600, "Micron Technology", "80CE042542F50C0000", "MB32G56U80S2R8.RtR", 2),
	}, map[string]map[string]string{
		"mc0/dimm2": {"dimm_label": "mc#0csrow#2channel#0", "dimm_location": "csrow 2 channel 0 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "16384", "dimm_ce_count": "0", "dimm_ue_count": "0"},
		"mc0/dimm3": {"dimm_label": "mc#0csrow#3channel#0", "dimm_location": "csrow 3 channel 0 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "16384", "dimm_ce_count": "0", "dimm_ue_count": "0"},
		"mc0/dimm6": {"dimm_label": "mc#0csrow#2channel#1", "dimm_location": "csrow 2 channel 1 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "16384", "dimm_ce_count": "4", "dimm_ue_count": "0"},
		"mc0/dimm7": {"dimm_label": "mc#0csrow#3channel#1", "dimm_location": "csrow 3 channel 1 ", "dimm_mem_type": "Unbuffered-DDR5", "size": "16384", "dimm_ce_count": "9083", "dimm_ue_count": "0"},
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
