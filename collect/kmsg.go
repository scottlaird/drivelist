package collect

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// KernelEvent is one kernel log line about a drive, classified.
type KernelEvent struct {
	At       time.Time
	Class    string // see the Class* constants
	DevName  string // "sdag", "nvme0n1"; "" when the line names only an address
	SCSIAddr string // "11:0:17:0"; "" for ATA and NVMe
	ATAPort  string // "ata3.00"; "" otherwise
	// SCSI sense: key, additional sense code and qualifier; -1 when absent.
	SenseKey, ASC, ASCQ int
	Location            string // for a hardware error, where: "mc0/csrow3/ch1"; "" otherwise
	Text                string // the message, without the kmsg header
}

// Host reports whether the event is about the host itself rather than a
// drive: a memory or machine-check error.
func (e KernelEvent) Host() bool { return e.Class == ClassHWCorrected || e.Class == ClassHWUncorrected }

// Code is the SCSI sense triple as "key:asc:ascq" in hex, the location of
// a hardware error, or "".
func (e KernelEvent) Code() string {
	if e.Location != "" {
		return e.Location
	}
	if e.SenseKey < 0 && e.ASC < 0 {
		return ""
	}
	k, a := "", ""
	if e.SenseKey >= 0 {
		k = strconv.FormatInt(int64(e.SenseKey), 16)
	}
	if e.ASC >= 0 {
		a = strconv.FormatInt(int64(e.ASC), 16)
	}
	if e.ASCQ < 0 {
		// The kernel decoded the code into text and the text gave only the
		// ASC; see addSense.
		return k + ":" + a
	}
	return k + ":" + a + ":" + strconv.FormatInt(int64(e.ASCQ), 16)
}

// Classes. Attach and detach are signals to re-inventory; the rest are
// counted per drive. See the design's classifier table.
const (
	ClassAttach            = "attach"
	ClassDetach            = "detach"
	ClassPredictiveFailure = "predictive_failure"
	ClassMediumError       = "medium_error"
	ClassHardwareError     = "hardware_error"
	ClassIOError           = "io_error"
	ClassTimeout           = "timeout"
	ClassLinkReset         = "link_reset"
	ClassRecovered         = "recovered"
	ClassWarning           = "warning" // the drive's own warning (ASC 0x0B): a vendor notice, a temperature, a background scan; not an I/O failure
	ClassOther             = "other"
	// About the host, not a drive: memory and machine-check errors, as
	// EDAC, the MCE decoder and APEI/GHES report them. Corrected ones are
	// the warning that a DIMM is failing; an uncorrected one usually
	// takes the machine down.
	ClassHWCorrected   = "hw_corrected"
	ClassHWUncorrected = "hw_uncorrected"
)

// Warning classes are the ones worth an event on first sight.
var WarningClasses = map[string]bool{
	ClassPredictiveFailure: true, ClassMediumError: true, ClassHardwareError: true, ClassIOError: true, ClassTimeout: true,
}

var senseKeys = map[string]int{
	"No Sense": 0, "Recovered Error": 1, "Not Ready": 2, "Medium Error": 3, "Hardware Error": 4,
	"Illegal Request": 5, "Unit Attention": 6, "Data Protect": 7, "Blank Check": 8,
	"Copy Aborted": 0xa, "Aborted Command": 0xb, "Volume Overflow": 0xd, "Miscompare": 0xe,
}

var (
	reSD       = regexp.MustCompile(`^sd (\d+:\d+:\d+:\d+): \[(\w+)\] (.*)$`)
	reSense    = regexp.MustCompile(`^tag#(\d+) Sense Key : ([A-Za-z ]+?) \[`)
	reASC      = regexp.MustCompile(`^tag#(\d+) ASC=0x([0-9a-fA-F]+) .*ASCQ=0x([0-9a-fA-F]+)`)
	reAddSense = regexp.MustCompile(`^tag#(\d+) Add\. Sense: (.*?)\s*$`)
	reBlk      = regexp.MustCompile(`(?:blk_update_request: )?(I/O error|critical medium error|critical target error), dev (\w+), sector`)
	reBuffer   = regexp.MustCompile(`^Buffer I/O error on dev (\w+)`)
	reATA      = regexp.MustCompile(`^(ata\d+(?:\.\d+)?): (.*)$`)
	reNVMeCtl  = regexp.MustCompile(`^nvme nvme(\d+): (.*)$`)
	reNVMeNS   = regexp.MustCompile(`^(nvme\d+n\d+): (.*)$`)
	reDevName  = regexp.MustCompile(`\b(sd[a-z]+|nvme\d+n\d+)(?:p?\d+)?\b`)
	reRemoving = regexp.MustCompile(`removing handle\(0x[0-9a-f]+\)`)
	// Hardware errors. EDAC: "EDAC MC0: 1 CE on mc#0csrow#3channel#1 (...)".
	// MCE decoder: "[Hardware Error]: Corrected error, no action required."
	// / "Uncorrected, software containable error." / "Deferred error, no
	// action required." APEI/GHES: "{1}[Hardware Error]: event severity:
	// corrected|recoverable|fatal". "mce: [Hardware Error]: CPU 3: Machine
	// Check Exception: ..." is the fatal Intel form.
	reEDAC    = regexp.MustCompile(`^EDAC MC(\d+): (\d+) (CE|UE) (?:.+ )?on ([^ (]+)`)
	reEDACLoc = regexp.MustCompile(`mc#(\d+)(?:csrow#(\d+))?(?:channel#(\d+))?`)
	reHWErr   = regexp.MustCompile(`\[Hardware Error\]: (.*)$`)
)

// classifier correlates the two lines a SCSI sense error is logged as, keyed
// by device address and command tag, and classifies everything else on
// sight. It is not safe for concurrent use.
type classifier struct {
	pending map[string]KernelEvent // "addr/tag" -> sense line waiting for its ASC line
}

func newClassifier() *classifier { return &classifier{pending: map[string]KernelEvent{}} }

// classify returns the event for a log line, or false if the line is not
// about a drive or is the first half of a pair.
func (c *classifier) classify(at time.Time, text string) (KernelEvent, bool) {
	ev := KernelEvent{At: at, Text: text, SenseKey: -1, ASC: -1, ASCQ: -1}

	if m := reEDAC.FindStringSubmatch(text); m != nil {
		ev.Class = ClassHWCorrected
		if m[3] == "UE" {
			ev.Class = ClassHWUncorrected
		}
		ev.Location = edacLocation(m[1], m[4])
		return ev, true
	}
	if m := reHWErr.FindStringSubmatch(text); m != nil {
		// One physical error is several lines; only the line that states
		// the severity counts, so the rest are dropped rather than
		// counted as "other".
		body := strings.ToLower(m[1])
		switch {
		case strings.HasPrefix(body, "corrected error"), strings.Contains(body, "event severity: corrected"):
			ev.Class = ClassHWCorrected
		case strings.HasPrefix(body, "uncorrected"), strings.HasPrefix(body, "deferred error"), strings.Contains(body, "machine check exception"),
			strings.Contains(body, "event severity: fatal"), strings.Contains(body, "event severity: recoverable"):
			ev.Class = ClassHWUncorrected
		default:
			return KernelEvent{}, false
		}
		return ev, true
	}

	if m := reSD.FindStringSubmatch(text); m != nil {
		ev.SCSIAddr, ev.DevName = m[1], m[2]
		body := m[3]
		switch {
		case strings.HasPrefix(body, "Attached SCSI disk"):
			ev.Class = ClassAttach
			return ev, true
		case strings.HasPrefix(body, "Synchronizing SCSI cache"), strings.Contains(body, "rejecting I/O to offline device"):
			ev.Class = ClassDetach
			return ev, true
		}
		if s := reSense.FindStringSubmatch(body); s != nil {
			ev.SenseKey = senseKeys[s[2]]
			if _, ok := senseKeys[s[2]]; !ok {
				ev.SenseKey = -1
			}
			c.pending[ev.SCSIAddr+"/"+s[1]] = ev
			return KernelEvent{}, false
		}
		if a := reASC.FindStringSubmatch(body); a != nil {
			key := ev.SCSIAddr + "/" + a[1]
			if first, ok := c.pending[key]; ok {
				ev.SenseKey = first.SenseKey
				ev.Text = first.Text + " | " + text
				delete(c.pending, key)
			}
			ev.ASC, ev.ASCQ = hexInt(a[2]), hexInt(a[3])
			ev.Class = senseClass(ev.SenseKey, ev.ASC, ev.ASCQ)
			return ev, true
		}
		// For a code it knows, the kernel prints the text instead of the
		// hex: "Add. Sense: Firmware impending failure seek error rate too
		// high". It is the second line of the pair all the same.
		if a := reAddSense.FindStringSubmatch(body); a != nil {
			key := ev.SCSIAddr + "/" + a[1]
			if first, ok := c.pending[key]; ok {
				ev.SenseKey = first.SenseKey
				ev.Text = first.Text + " | " + text
				delete(c.pending, key)
			}
			ev.ASC = addSense(a[2])
			ev.Class = senseClass(ev.SenseKey, ev.ASC, -1)
			return ev, true
		}
		if strings.Contains(body, "Unrecovered read error") {
			ev.Class = ClassMediumError
			return ev, true
		}
		ev.Class = ClassOther
		return ev, true
	}

	if m := reBlk.FindStringSubmatch(text); m != nil {
		ev.DevName = diskName(m[2])
		ev.Class = ClassIOError
		if strings.Contains(m[1], "medium") {
			ev.Class = ClassMediumError
		}
		return ev, true
	}
	if m := reBuffer.FindStringSubmatch(text); m != nil {
		ev.DevName, ev.Class = diskName(m[1]), ClassIOError
		return ev, true
	}
	if m := reATA.FindStringSubmatch(text); m != nil {
		ev.ATAPort = m[1]
		body := m[2]
		switch {
		case strings.HasPrefix(body, "exception Emask"), strings.Contains(body, "failed command"):
			ev.Class = ClassTimeout
		case strings.Contains(body, "hard resetting link"), strings.Contains(body, "soft resetting link"), strings.Contains(body, "link is slow to respond"):
			ev.Class = ClassLinkReset
		case strings.Contains(body, "{ UNC }"):
			ev.Class = ClassMediumError
		case strings.Contains(body, "ATA-"), strings.Contains(body, "configured for"):
			return KernelEvent{}, false // probe chatter at attach; the sd line covers it
		default:
			ev.Class = ClassOther
		}
		return ev, true
	}
	if m := reNVMeCtl.FindStringSubmatch(text); m != nil {
		ev.DevName = "nvme" + m[1]
		body := m[2]
		switch {
		case strings.Contains(body, "timeout"), strings.Contains(body, "reset controller"), strings.Contains(body, "resetting controller"):
			ev.Class = ClassTimeout
		case strings.Contains(body, "Removing"), strings.Contains(body, "removing"):
			ev.Class = ClassDetach
		case strings.Contains(body, "new subsystem"), strings.Contains(body, "allocated"), strings.Contains(body, "queues"):
			ev.Class = ClassAttach
		default:
			ev.Class = ClassOther
		}
		return ev, true
	}
	if m := reNVMeNS.FindStringSubmatch(text); m != nil {
		ev.DevName = m[1]
		ev.Class = ClassIOError
		if !strings.Contains(m[2], "Error") && !strings.Contains(m[2], "error") {
			ev.Class = ClassOther
		}
		return ev, true
	}
	if reRemoving.MatchString(text) {
		ev.Class = ClassDetach
		return ev, true
	}
	if m := reDevName.FindStringSubmatch(text); m != nil {
		ev.DevName, ev.Class = m[1], ClassOther
		return ev, true
	}
	return KernelEvent{}, false
}

// edacLocation renders EDAC's "mc#0csrow#3channel#1" as "mc0/csrow3/ch1";
// a location in another form (a DIMM label) is kept as it is.
func edacLocation(mc, loc string) string {
	m := reEDACLoc.FindStringSubmatch(loc)
	if m == nil {
		return "mc" + mc + "/" + loc
	}
	out := "mc" + m[1]
	if m[2] != "" {
		out += "/csrow" + m[2]
	}
	if m[3] != "" {
		out += "/ch" + m[3]
	}
	return out
}

// senseClass keys on the additional sense code first: 0x5D is "failure
// prediction threshold exceeded" whatever the sense key says.
func senseClass(key, asc, ascq int) string {
	if asc == 0x5d {
		return ClassPredictiveFailure
	}
	if asc == 0x11 { // unrecovered read error, whatever the key says
		return ClassMediumError
	}
	// ASC 0x0B is WARNING: the drive telling on itself, not a failed
	// command. The standard qualifiers that mean the medium or the drive
	// is going are classed as such; the rest, vendor ones included (HGST
	// and Oracle firmware sends 0x96 and 0x97 around reallocations), are
	// warnings, worth a SMART sample and a count, not an error.
	if asc == 0x0b {
		switch ascq {
		case 0x03: // background self-test failed
			return ClassPredictiveFailure
		case 0x04, 0x05: // background pre-scan / medium scan found a medium error
			return ClassMediumError
		}
		return ClassWarning
	}
	switch key {
	case 1:
		return ClassRecovered
	case 3:
		return ClassMediumError
	case 4:
		return ClassHardwareError
	case 0xb:
		return ClassTimeout
	case 2, 6:
		return ClassOther
	}
	return ClassOther
}

// addSense is the ASC behind the text the kernel prints for a code it
// knows, for the families that matter here; -1 for the rest, which are
// then classed by the sense key alone.
func addSense(text string) int {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "impending failure"), strings.Contains(t, "prediction threshold"):
		return 0x5d
	case strings.HasPrefix(t, "recovered data with error correction"):
		return 0x18
	case strings.HasPrefix(t, "recovered data without ecc"), strings.HasPrefix(t, "recovered data"):
		return 0x17
	case strings.HasPrefix(t, "unrecovered read error"):
		return 0x11
	case strings.HasPrefix(t, "write error"):
		return 0x0c
	case strings.HasPrefix(t, "warning"):
		return 0x0b
	case strings.HasPrefix(t, "defect list"), strings.HasPrefix(t, "medium format corrupted"):
		return 0x31
	}
	return -1
}

// diskName strips a partition suffix: sdc1 -> sdc, nvme0n1p2 -> nvme0n1.
func diskName(dev string) string {
	if strings.HasPrefix(dev, "nvme") {
		if i := strings.LastIndex(dev, "p"); i > 0 {
			return dev[:i]
		}
		return dev
	}
	return strings.TrimRight(dev, "0123456789")
}

func hexInt(h string) int {
	v, err := strconv.ParseInt(h, 16, 32)
	if err != nil {
		return -1
	}
	return int(v)
}

// kmsgPath is the kernel log ring buffer as a stream. Reading it needs
// root (or dmesg_restrict=0); each read returns one record.
const kmsgPath = "/dev/kmsg"

// OpenKernelLog opens /dev/kmsg positioned after the last existing record,
// so only new messages are seen.
func OpenKernelLog() (io.ReadCloser, error) {
	f, err := os.Open(kmsgPath)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// FollowKernelLog reads kmsg-format records from r until ctx ends or r
// fails, classifying each and sending the ones about drives to out. It
// closes r when ctx ends so a blocked read returns. The kmsg format is
// "prio,seq,usec,flags[,…];message", with continuation lines (leading
// space) that are ignored; a plain "message" line without a header is
// accepted too, which is what tests and captured logs feed it.
func FollowKernelLog(ctx context.Context, r io.ReadCloser, now func() time.Time, out chan<- KernelEvent) error {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			r.Close()
		case <-done:
		}
	}()
	c := newClassifier()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == ' ' {
			continue
		}
		text := line
		if _, msg, ok := strings.Cut(line, ";"); ok && strings.Count(strings.SplitN(line, ";", 2)[0], ",") >= 2 {
			text = msg
		}
		text = stripTimestamp(text)
		ev, ok := c.classify(now(), text)
		if !ok {
			continue
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			return nil
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := sc.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	return io.EOF
}

// stripTimestamp drops a leading "[12345.678901] " as dmesg prints, so
// captured dmesg output classifies like live kmsg.
func stripTimestamp(s string) string {
	if strings.HasPrefix(s, "[") {
		if i := strings.Index(s, "] "); i > 0 && i < 20 {
			return s[i+2:]
		}
	}
	return s
}
