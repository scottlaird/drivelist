package collect

import (
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func readOpticFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "optics", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func near(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 1e-6 }

// TestParseSFF8472: an SFP+ SR with its receiver in alarm. One lane,
// power in mW, the module's own thresholds, and the flags it raised.
func TestParseSFF8472(t *testing.T) {
	o, ok := ParseEthtoolModule(readOpticFixture(t, "sff8472-sr.txt"))
	if !ok {
		t.Fatal("not parsed")
	}
	if o.Form != "SFP" || o.Vendor != "FINISAR CORP." || o.Part != "FTLX8571D3BCL" || o.Serial != "AL4N8TB" || o.Rev != "A" || o.OUI != "00:90:65" || o.DateCode != "141120" {
		t.Errorf("identity = %+v", o)
	}
	if o.Compliance != "10G Ethernet: 10G Base-SR" || o.Connector != "LC" || o.Wavelength != 850 || o.Kind != "optical" || !o.Diagnostics {
		t.Errorf("type = %q %q %v %q %v", o.Compliance, o.Connector, o.Wavelength, o.Kind, o.Diagnostics)
	}
	if !near(o.TempC, 29.93) || !near(o.VoltageV, 3.3219) {
		t.Errorf("temp %v voltage %v", o.TempC, o.VoltageV)
	}
	if len(o.Lanes) != 1 || o.Lanes[0].Lane != 1 || !near(o.Lanes[0].BiasMA, 7.508) || !near(o.Lanes[0].TxMW, 0.5937) || !near(o.Lanes[0].RxMW, 0.018) {
		t.Errorf("lanes = %+v", o.Lanes)
	}
	want := map[string]float64{"bias_high_alarm": 13.2, "bias_low_warning": 5, "tx_high_alarm": 1, "tx_low_warning": 0.3162,
		"temp_high_alarm": 78, "temp_high_warning": 73, "voltage_low_alarm": 2.9, "rx_low_alarm": 0.02, "rx_low_warning": 0.0398}
	if !reflect.DeepEqual(o.Thresholds, want) {
		t.Errorf("thresholds = %v", o.Thresholds)
	}
	if !reflect.DeepEqual(o.Flags, []string{"rx low alarm", "rx low warning"}) {
		t.Errorf("flags = %v", o.Flags)
	}
	if d := DBm(o.Lanes[0].RxMW); d == nil || math.Abs(*d-(-17.447)) > 0.01 {
		t.Errorf("rx dBm = %v", d)
	}
}

// TestParseSFF8636: a QSFP28 SR4, four lanes, lane 3's receiver low and
// flagged; SFF-8636's labels ("Rcvr signal avg optical power(Channel 3)",
// with no space) and its per-lane flags ("(Chan 3)").
func TestParseSFF8636(t *testing.T) {
	o, ok := ParseEthtoolModule(readOpticFixture(t, "sff8636-sr4.txt"))
	if !ok {
		t.Fatal("not parsed")
	}
	if o.Form != "QSFP28" || o.Part != "MMA1B00-C100D" || o.Serial != "MT1918FT01234" || o.Connector != "MPO Parallel Optic" || o.Kind != "optical" {
		t.Errorf("identity = %+v", o)
	}
	if len(o.Lanes) != 4 {
		t.Fatalf("lanes = %+v", o.Lanes)
	}
	for i, l := range o.Lanes {
		if l.Lane != i+1 || l.BiasMA == nil || l.TxMW == nil || l.RxMW == nil {
			t.Errorf("lane %d = %+v", i+1, l)
		}
	}
	if !near(o.Lanes[2].RxMW, 0.05) || !near(o.Lanes[3].BiasMA, 6.9) || !near(o.Lanes[1].TxMW, 0.79) {
		t.Errorf("lane values = %+v", o.Lanes)
	}
	if !reflect.DeepEqual(o.Flags, []string{"rx low warning lane 3"}) {
		t.Errorf("flags = %v", o.Flags)
	}
	if o.Thresholds["rx_low_warning"] != 0.1 || o.Thresholds["tx_low_warning"] != 0.1995 || o.Thresholds["temp_high_warning"] != 70 {
		t.Errorf("thresholds = %v", o.Thresholds)
	}
}

// TestParseDAC: a passive QSFP28 copper cable: identity, no readings.
func TestParseDAC(t *testing.T) {
	o, ok := ParseEthtoolModule(readOpticFixture(t, "sff8636-dac.txt"))
	if !ok {
		t.Fatal("not parsed")
	}
	if o.Kind != "dac" || o.Diagnostics || len(o.Lanes) != 0 || o.TempC != nil || o.Part != "MCP1600-C001" || o.Compliance != "100G Ethernet: 100G Base-CR4 or 25G Base-CR CA-L" {
		t.Errorf("DAC = %+v", o)
	}
}

// TestParseCMIS: a QSFP-DD, eight lanes (two shown), CMIS's labels, and a
// line with "power" in it that is not a measurement.
func TestParseCMIS(t *testing.T) {
	o, ok := ParseEthtoolModule(readOpticFixture(t, "cmis-qsfpdd.txt"))
	if !ok {
		t.Fatal("not parsed")
	}
	if o.Form != "QSFP-DD" || o.Vendor != "INNOLIGHT" || o.Serial != "INKBF1230123" || o.Compliance != "850 nm VCSEL" || o.Connector != "MPO 1x16" || o.Kind != "optical" {
		t.Errorf("identity = %+v", o)
	}
	if len(o.Lanes) != 2 || o.Lanes[1].Lane != 8 || !near(o.Lanes[1].RxMW, 0.68) || !near(o.Lanes[0].TxMW, 0.8) || !near(o.TempC, 45.4) {
		t.Errorf("lanes = %+v temp %v", o.Lanes, o.TempC)
	}
	if len(o.Flags) != 0 || o.Thresholds["temp_high_alarm"] != 75 {
		t.Errorf("flags %v thresholds %v", o.Flags, o.Thresholds)
	}
}

func TestParseNotAModule(t *testing.T) {
	if _, ok := ParseEthtoolModule("netlink error: Operation not supported\n"); ok {
		t.Error("an error message parsed as a module")
	}
}

// TestOptics: ports are read in natural order; a port with no module,
// and a virtual interface, are skipped; a QSFP split into four breakouts
// is one optic listed under the first; a missing ethtool is an error.
func TestOptics(t *testing.T) {
	sys := t.TempDir()
	ports := map[string]string{
		"swp1s0": "sff8636-sr4.txt", "swp1s1": "sff8636-sr4.txt", "swp1s2": "sff8636-sr4.txt", "swp1s3": "sff8636-sr4.txt",
		"swp10": "sff8636-dac.txt", "swp2": "", "enp1s0f0": "sff8472-sr.txt",
	}
	for name := range ports {
		if err := os.MkdirAll(filepath.Join(sys, "class", "net", name, "device"), 0o755); err != nil {
			t.Fatal(err)
		}
		up := "down"
		if name == "swp1s2" || name == "enp1s0f0" {
			up = "up"
		}
		os.WriteFile(filepath.Join(sys, "class", "net", name, "operstate"), []byte(up+"\n"), 0o644)
		if strings.HasPrefix(name, "swp1s") {
			os.WriteFile(filepath.Join(sys, "class", "net", name, "carrier_changes"), []byte("3\n"), 0o644)
		}
	}
	os.MkdirAll(filepath.Join(sys, "class", "net", "bond0"), 0o755) // no device: virtual
	var asked []string
	c := &Collector{Platform: "linux", Sys: sys, Exec: func(name string, args ...string) ([]byte, error) {
		port := args[len(args)-1]
		asked = append(asked, port)
		if ports[port] == "" {
			return nil, errors.New("exit status 71")
		}
		return os.ReadFile(filepath.Join("testdata", "optics", ports[port]))
	}}
	got, err := c.Optics()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"enp1s0f0", "swp1s0", "swp1s1", "swp1s2", "swp1s3", "swp2", "swp10"}; !reflect.DeepEqual(asked, want) {
		t.Errorf("ports read = %v, want %v", asked, want)
	}
	if len(got) != 3 {
		t.Fatalf("optics = %+v", got)
	}
	if got[0].Port != "enp1s0f0" || got[0].Link != "up" || got[0].Form != "SFP" {
		t.Errorf("SFP = %+v", got[0])
	}
	if got[1].Port != "swp1s0" || !reflect.DeepEqual(got[1].Ports, []string{"swp1s0", "swp1s1", "swp1s2", "swp1s3"}) || got[1].Link != "up" || got[1].CarrierChanges == nil || *got[1].CarrierChanges != 12 {
		t.Errorf("breakout = %+v", got[1])
	}
	if got[2].Port != "swp10" || got[2].Kind != "dac" || got[2].CarrierChanges != nil {
		t.Errorf("DAC = %+v", got[2])
	}

	c.Exec = func(string, ...string) ([]byte, error) { return nil, exec.ErrNotFound }
	if _, err := c.Optics(); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("missing ethtool: err = %v", err)
	}
}

func TestNaturalLess(t *testing.T) {
	for _, tc := range [][2]string{{"eth2", "eth10"}, {"swp1s0", "swp1s1"}, {"swp1s3", "swp2"}, {"enp1s0f0", "enp1s0f1"}, {"eno1", "enp1s0"}} {
		if !naturalLess(tc[0], tc[1]) || naturalLess(tc[1], tc[0]) {
			t.Errorf("naturalLess(%q, %q) wrong", tc[0], tc[1])
		}
	}
}
