package collect

import (
	"errors"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Optic is one pluggable module in a network port: an SFP, a QSFP, a
// QSFP-DD or OSFP, optical or copper, as ethtool -m decodes its EEPROM
// (SFF-8472, SFF-8636 or CMIS). Measurements are pointers because a
// module that does not report one (a passive DAC reports none) is not
// a module reading zero.
type Optic struct {
	Port  string   // the interface the module was read through: "enp1s0f0", "swp7"
	Ports []string // every interface that reads the same module, for a port split into breakouts

	Form       string // the first word of the identifier: "SFP", "QSFP28", "QSFP-DD", "OSFP"
	Identifier string // as ethtool prints it: "0x11 (QSFP28)"
	Kind       string // "optical" | "aoc" | "dac" | "": what the connector and diagnostics say
	Vendor     string
	OUI        string
	Part       string
	Rev        string
	Serial     string
	DateCode   string // "YYMMDD" and an optional lot code
	Compliance string // the first transceiver or media type line: "10G Ethernet: 10G Base-SR"
	Connector  string // "LC", "MPO 1x12", "No separable connector"
	Wavelength float64

	Link string // the interface's operstate: "up" | "down" | ...
	// CarrierChanges is the kernel's count of link up and down
	// transitions on the port since the interface was created, summed
	// over breakouts; nil when unreadable. Its growth between readings
	// is how often the link flapped.
	CarrierChanges *uint64
	Diagnostics    bool     // the module reports any measurement at all
	TempC          *float64 // module temperature
	VoltageV       *float64 // supply voltage
	Lanes          []OpticLane
	// Thresholds are the module's own, from its EEPROM: "rx_low_warning",
	// "temp_high_alarm", ... in the units of the measurement (mW for
	// power, mA for bias, °C, V).
	Thresholds map[string]float64
	// Flags are the alarm and warning flags the module has raised now:
	// "rx low warning lane 2", "temp high alarm".
	Flags []string
}

// OpticLane is one lane's (channel's) measurements. A single-lane SFP
// has lane 1; a QSFP has 1 to 4; CMIS modules up to 8 per bank.
type OpticLane struct {
	Lane   int
	BiasMA *float64
	TxMW   *float64
	RxMW   *float64
}

// Key is what an optic is identified by across ports and hosts:
// vendor, part and serial. It is "" for a module with no serial number,
// which cannot be followed when it moves.
func (o Optic) Key() string {
	if o.Serial == "" {
		return ""
	}
	return strings.Join([]string{o.Vendor, o.Part, o.Serial}, "|")
}

// DBm converts milliwatts to dBm; nil for nil or a reading of zero,
// which is below anything a receiver can report.
func DBm(mw *float64) *float64 {
	if mw == nil || *mw <= 0 {
		return nil
	}
	v := 10 * math.Log10(*mw)
	return &v
}

// Optics reads the module in every physical network port: interfaces
// with a device under /sys/class/net, each read with ethtool -m. A port
// with no module, or one ethtool cannot read, is left out. Several
// interfaces reading the same module (a port split into breakouts) are
// one optic, listed under the first. It fails only when ethtool itself
// is missing; everything else is a port without an optic.
func (c *Collector) Optics() ([]Optic, error) {
	if c.platform() != "linux" {
		return nil, nil
	}
	var out []Optic
	byKey := map[string]int{}
	for _, name := range c.netPorts() {
		raw, err := c.run("ethtool", "-m", name)
		if errors.Is(err, exec.ErrNotFound) {
			return nil, err
		}
		if err != nil || len(raw) == 0 {
			continue
		}
		o, ok := ParseEthtoolModule(string(raw))
		if !ok {
			continue
		}
		o.Port, o.Ports = name, []string{name}
		if b, err := os.ReadFile(filepath.Join(c.sys(), "class", "net", name, "operstate")); err == nil {
			o.Link = strings.TrimSpace(string(b))
		}
		o.CarrierChanges = c.carrierChanges(name)
		if k := o.Key(); k != "" {
			if i, seen := byKey[k]; seen {
				out[i].Ports = append(out[i].Ports, name)
				if o.Link == "up" {
					out[i].Link = "up"
				}
				if o.CarrierChanges != nil && out[i].CarrierChanges != nil {
					sum := *out[i].CarrierChanges + *o.CarrierChanges
					out[i].CarrierChanges = &sum
				}
				continue
			}
			byKey[k] = len(out)
		}
		out = append(out, o)
	}
	return out, nil
}

// carrierChanges reads /sys/class/net/PORT/carrier_changes; nil when
// the kernel does not have it (before 3.15) or it is unreadable.
func (c *Collector) carrierChanges(port string) *uint64 {
	b, err := os.ReadFile(filepath.Join(c.sys(), "class", "net", port, "carrier_changes"))
	if err != nil {
		return nil
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// netPorts lists the physical network interfaces, the ones with a
// device, in natural order: eth2 before eth10, swp1s0 before swp1s1.
func (c *Collector) netPorts() []string {
	entries, err := os.ReadDir(filepath.Join(c.sys(), "class", "net"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if name == "lo" {
			continue
		}
		if _, err := os.Stat(filepath.Join(c.sys(), "class", "net", name, "device")); err != nil {
			continue
		}
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })
	return names
}

var reDigits = regexp.MustCompile(`\d+|\D+`)

// NaturalLess orders strings with their digit runs compared as numbers:
// port names in the order a person would list them.
func NaturalLess(a, b string) bool { return naturalLess(a, b) }

// naturalLess orders strings with their digit runs compared as numbers.
func naturalLess(a, b string) bool {
	pa, pb := reDigits.FindAllString(a, -1), reDigits.FindAllString(b, -1)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] == pb[i] {
			continue
		}
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		if ea == nil && eb == nil {
			if na != nb {
				return na < nb
			}
			continue
		}
		return pa[i] < pb[i]
	}
	return len(pa) < len(pb)
}

var (
	reChannel = regexp.MustCompile(`(?i)\(\s*(?:channel|chan|lane)\s*(\d+)\s*\)`)
	reNumber  = regexp.MustCompile(`-?\d+(?:\.\d+)?`)
	reMW      = regexp.MustCompile(`(-?\d+(?:\.\d+)?)\s*mW`)
	reDBm     = regexp.MustCompile(`(-?\d+(?:\.\d+)?|-inf)\s*dBm`)
	reParen   = regexp.MustCompile(`\(([^)]*)\)`)
	reSpaces  = regexp.MustCompile(`\s+`)
)

// ParseEthtoolModule reads the text ethtool -m prints for any of the
// three standards. It keys on words rather than exact labels, since the
// labels differ between standards ("Receiver signal average optical
// power" in SFF-8472, "Rcvr signal avg optical power" in SFF-8636 and
// CMIS) and between ethtool versions. It returns false when the text
// names no module.
func ParseEthtoolModule(text string) (Optic, bool) {
	o := Optic{Thresholds: map[string]float64{}}
	lanes := map[int]*OpticLane{}
	lane := func(n int) *OpticLane {
		if n <= 0 {
			n = 1
		}
		if lanes[n] == nil {
			lanes[n] = &OpticLane{Lane: n}
		}
		return lanes[n]
	}
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		ch := 0
		if m := reChannel.FindStringSubmatch(k); m != nil {
			ch, _ = strconv.Atoi(m[1])
			k = reChannel.ReplaceAllString(k, " ")
		}
		key := strings.ToLower(strings.TrimSpace(reSpaces.ReplaceAllString(k, " ")))
		switch key {
		case "identifier":
			o.Identifier = v
			if m := reParen.FindStringSubmatch(v); m != nil {
				if f := strings.Fields(m[1]); len(f) > 0 {
					o.Form = f[0]
				}
			}
			continue
		case "vendor name":
			o.Vendor = v
			continue
		case "vendor oui":
			o.OUI = v
			continue
		case "vendor pn":
			o.Part = v
			continue
		case "vendor rev":
			o.Rev = v
			continue
		case "vendor sn":
			o.Serial = v
			continue
		case "date code":
			o.DateCode = v
			continue
		case "connector":
			o.Connector = v
			if m := reParen.FindStringSubmatch(v); m != nil {
				o.Connector = m[1]
			}
			continue
		case "transceiver type", "media interface technology", "transmitter technology":
			if o.Compliance == "" {
				o.Compliance = v
				if m := reParen.FindStringSubmatch(v); m != nil && strings.HasPrefix(v, "0x") {
					o.Compliance = m[1]
				}
			}
			continue
		case "laser wavelength":
			if n := reNumber.FindString(v); n != "" {
				o.Wavelength, _ = strconv.ParseFloat(n, 64)
			}
			continue
		case "alarm/warning flags implemented", "optical diagnostics support":
			continue
		}
		q := quantity(key)
		if q == "" {
			continue
		}
		switch {
		case strings.HasSuffix(key, " threshold"):
			if lvl := level(key); lvl != "" {
				if x, ok := measure(q, v); ok {
					o.Thresholds[q+"_"+lvl] = x
				}
			}
		case strings.HasSuffix(key, " alarm") || strings.HasSuffix(key, " warning"):
			if strings.EqualFold(v, "on") {
				flag := q + " " + strings.ReplaceAll(level(key), "_", " ")
				if ch > 0 && (q == "bias" || q == "tx" || q == "rx") {
					flag += " lane " + strconv.Itoa(ch)
				}
				o.Flags = append(o.Flags, flag)
			}
		default:
			x, ok := measure(q, v)
			if !ok {
				continue
			}
			o.Diagnostics = true
			switch q {
			case "temp":
				o.TempC = &x
			case "voltage":
				o.VoltageV = &x
			case "bias":
				lane(ch).BiasMA = &x
			case "tx":
				lane(ch).TxMW = &x
			case "rx":
				lane(ch).RxMW = &x
			}
		}
	}
	if o.Identifier == "" && o.Vendor == "" && o.Part == "" && o.Serial == "" {
		return Optic{}, false
	}
	for _, l := range lanes {
		o.Lanes = append(o.Lanes, *l)
	}
	sort.Slice(o.Lanes, func(i, j int) bool { return o.Lanes[i].Lane < o.Lanes[j].Lane })
	o.Kind = opticKind(o)
	if len(o.Thresholds) == 0 {
		o.Thresholds = nil
	}
	return o, true
}

// quantity names what a label measures: temp, voltage, bias, tx or rx
// power; "" for anything else. Bias is checked first because its label
// often says "tx" too ("Laser tx bias current").
func quantity(key string) string {
	switch {
	case strings.Contains(key, "temperature"):
		return "temp"
	case strings.Contains(key, "voltage"):
		return "voltage"
	case strings.Contains(key, "bias"):
		return "bias"
	case strings.Contains(key, "power") && (strings.Contains(key, "rx") || strings.Contains(key, "rcvr") || strings.Contains(key, "receiver")):
		return "rx"
	case strings.Contains(key, "power") && (strings.Contains(key, "output") || strings.Contains(key, "transmit") || strings.Contains(key, "tx")):
		return "tx"
	}
	return ""
}

// level is "high_alarm", "low_warning" and so on, from a threshold or
// flag label; "" when the label has neither.
func level(key string) string {
	hl := ""
	switch {
	case strings.Contains(key, " high "):
		hl = "high"
	case strings.Contains(key, " low "):
		hl = "low"
	default:
		return ""
	}
	switch {
	case strings.Contains(key, "alarm"):
		return hl + "_alarm"
	case strings.Contains(key, "warning"):
		return hl + "_warning"
	}
	return ""
}

// measure reads a value in the quantity's unit: power in mW (from the mW
// figure, or converted from dBm when that is all there is), everything
// else the first number.
func measure(q, v string) (float64, bool) {
	if q == "tx" || q == "rx" {
		if m := reMW.FindStringSubmatch(v); m != nil {
			x, err := strconv.ParseFloat(m[1], 64)
			return x, err == nil
		}
		if m := reDBm.FindStringSubmatch(v); m != nil {
			if m[1] == "-inf" {
				return 0, true
			}
			x, err := strconv.ParseFloat(m[1], 64)
			return math.Pow(10, x/10), err == nil
		}
		return 0, false
	}
	n := reNumber.FindString(v)
	if n == "" {
		return 0, false
	}
	x, err := strconv.ParseFloat(n, 64)
	return x, err == nil
}

// opticKind guesses what the module is: a direct-attach copper cable
// (copper connector or a CR compliance, and no optical readings), an
// active optical cable (no separable connector but optical readings),
// or an optical transceiver.
func opticKind(o Optic) string {
	conn := strings.ToLower(o.Connector)
	comp := strings.ToLower(o.Compliance)
	optical := false
	for _, l := range o.Lanes {
		if l.RxMW != nil || l.TxMW != nil {
			optical = true
		}
	}
	switch {
	case strings.Contains(conn, "copper") || strings.Contains(comp, "copper") || strings.Contains(comp, "base-cr") || strings.Contains(comp, "cr4"):
		if optical {
			return "aoc"
		}
		return "dac"
	case strings.Contains(conn, "no separable"):
		if optical {
			return "aoc"
		}
		return "dac"
	case optical || conn != "":
		return "optical"
	}
	return ""
}

// captureOptics records ethtool -m for every physical port, and the
// marks netPorts and the operstate need, into a fixture.
func (c *Collector) captureOptics(dir string) error {
	for _, name := range c.netPorts() {
		if err := os.MkdirAll(filepath.Join(dir, "sys", "class", "net", name, "device"), 0o755); err != nil {
			return err
		}
		for _, attr := range []string{"operstate", "carrier_changes"} {
			if err := c.copySys(dir, "/class/net/"+name+"/"+attr); err != nil {
				slog.Debug("capture: net attribute", "port", name, "attr", attr, "err", err)
			}
		}
		out, err := c.run("ethtool", "-m", name)
		if err != nil || len(out) == 0 {
			continue
		}
		if err := c.captureExec(dir, out, "ethtool", "-m", name); err != nil {
			return err
		}
	}
	return nil
}
