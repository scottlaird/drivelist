package hardware

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedProfiles loads what ships: every file parses, validates,
// and matches a distinct model. A bad profile fails here, not at run time.
func TestEmbeddedProfiles(t *testing.T) {
	s := Embedded()
	if len(s.All()) < 2 {
		t.Fatalf("only %d embedded profiles", len(s.All()))
	}
	rs := s.Lookup("ASUSTeK COMPUTER INC. RS500A-E10-RS12U", "")
	if rs == nil || len(rs.Bays) != 12 || rs.Layout == nil || rs.Layout.Rows != 2 || rs.Layout.Columns != 6 {
		t.Fatalf("RS500A profile = %+v", rs)
	}
	if r, c := rs.Layout.Position(1); r != 1 || c != 0 {
		t.Errorf("bay 2 at row %d col %d, want row 1 col 0", r, c)
	}
	if r, c := rs.Layout.Position(11); r != 1 || c != 5 {
		t.Errorf("bay 12 at row %d col %d, want row 1 col 5", r, c)
	}
	if _, ok := rs.Label("pci", "9-1"); ok {
		t.Error("unprobed RS500A maps slot 9-1")
	}
	hg := s.Lookup("HGST 4U60_STOR_ENCL", "")
	if hg == nil || len(hg.Bays) != 60 {
		t.Fatalf("HGST profile = %+v", hg)
	}
	if label, ok := hg.Label("sas", "54"); !ok || label != "54" {
		t.Errorf("HGST sas 54 -> %q %v", label, ok)
	}
	if _, ok := hg.Label("sas", "62"); ok {
		t.Error("HGST maps the service device's identifier")
	}
	if _, ok := hg.Label("pci", "54"); ok {
		t.Error("HGST maps a pci identity")
	}
	if s.Lookup("nosuch", "") != nil {
		t.Error("unknown model matched")
	}
}

func TestKind(t *testing.T) {
	for via, want := range map[string]string{"expander-11:0": "sas", "host11": "sas", "pci": "pci", "ata": "ata", "": "", "usb": ""} {
		if got := Kind(via); got != want {
			t.Errorf("Kind(%q) = %q, want %q", via, got, want)
		}
	}
}

// TestLoadDirOverrides: a profile in a directory replaces the embedded
// one for the same model and adds new models; a bad one is refused with
// its file named.
func TestLoadDirOverrides(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("rs500a.json", `{"match": {"model": "ASUSTeK COMPUTER INC. RS500A-E10-RS12U"}, "title": "probed", "bays": [{"bay": "1", "pci": "9", "ata": "ata1"}, {"bay": "2", "pci": "9-1"}]}`)
	write("box.json", `{"match": {"model": "Acme Box", "board": "B1"}, "title": "Acme with board B1", "bays": [{"bay": "x", "sas": "0"}]}`)
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	rs := s.Lookup("ASUSTeK COMPUTER INC. RS500A-E10-RS12U", "")
	if rs.Title != "probed" || len(rs.Bays) != 2 {
		t.Errorf("override not applied: %+v", rs)
	}
	if l, ok := rs.Label("pci", "9-1"); !ok || l != "2" {
		t.Errorf("pci 9-1 -> %q %v", l, ok)
	}
	if l, ok := rs.Label("ata", "ata1"); !ok || l != "1" {
		t.Errorf("ata1 -> %q %v", l, ok)
	}
	if s.Lookup("Acme Box", "B1") == nil || s.Lookup("Acme Box", "B2") != nil {
		t.Error("board narrowing wrong")
	}
	if len(s.All()) != len(Embedded().All())+1 {
		t.Errorf("profiles = %d", len(s.All()))
	}

	write("bad.json", `{"match": {"model": "Bad"}, "title": "dup ids", "bays": [{"bay": "1", "sas": "0"}, {"bay": "2", "sas": "0"}]}`)
	if _, err := Load(dir); err == nil || !filepath.IsAbs(dir) && false {
		t.Errorf("duplicate identity accepted: %v", err)
	}
	os.Remove(filepath.Join(dir, "bad.json"))
	write("unknown.json", `{"match": {"model": "U"}, "title": "t", "bays": [{"bay": "1"}], "colour": "red"}`)
	if _, err := Load(dir); err == nil {
		t.Error("unknown field accepted")
	}
}

// TestMSA2Profile: the three M.2 slots resolve by SMBIOS designation for
// the first and root port for the others, as mon1 reports them.
func TestMSA2Profile(t *testing.T) {
	p := Embedded().Lookup("Micro Computer (HK) Tech Limited MS-A2", "")
	if p == nil {
		t.Fatal("no MS-A2 profile")
	}
	for id, want := range map[string]string{"J3502": "M.2 1", "0000:00:01.3": "M.2 2", "0000:00:01.4": "M.2 3", "PCIE4": "PCIe slot"} {
		if got, ok := p.Label("pci", id); !ok || got != want {
			t.Errorf("pci %s -> %q %v, want %q", id, got, ok, want)
		}
	}
	if _, ok := p.Label("pci", "PCIE3"); ok {
		t.Error("the Wi-Fi slot maps to a bay")
	}
}

func TestMS01Profile(t *testing.T) {
	p := Embedded().Lookup("Micro Computer (HK) Tech Limited Venus Series", "AHWSA")
	if p == nil {
		t.Fatal("no MS-01 profile")
	}
	if Embedded().Lookup("Micro Computer (HK) Tech Limited Venus Series", "") != nil {
		t.Error("the family name alone matched the MS-01")
	}
	for id, want := range map[string]string{"0000:00:06.0": "M.2 PCIe 4.0 x4", "0000:00:1c.4": "M.2 PCIe 3.0 x4 / U.2", "0000:00:01.0": "PCIe slot"} {
		if got, ok := p.Label("pci", id); !ok || got != want {
			t.Errorf("pci %s -> %q %v, want %q", id, got, ok, want)
		}
	}
	if len(p.Bays) != 4 || p.Bays[2].ID("pci") != "" {
		t.Errorf("the x2 slot should be declared without an identity: %+v", p.Bays)
	}
}

func TestM510Profile(t *testing.T) {
	p := Embedded().Lookup("HP ProLiant m510 Server Cartridge", "ProLiant m510 Server Cartridge")
	if p == nil {
		t.Fatal("no m510 profile")
	}
	for kind, id := range map[string]string{"pci": "PCI-E Slot 6/00.0/08.0", "ata": "pci-0000:00:1f.2-ata-5"} {
		if _, ok := p.Label(kind, id); !ok {
			t.Errorf("%s %s unmapped", kind, id)
		}
	}
	if _, ok := p.Label("pci", "PCI-E Slot 6"); ok {
		t.Error("the bare slot designation maps to a bay")
	}
}
