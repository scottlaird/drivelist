package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// SmartSummary is the handful of SMART facts worth keeping per sample,
// with the three dialects (SATA attributes, SAS log pages, the NVMe health
// log) mapped onto the same fields. A nil pointer means the drive's
// dialect does not report that value; it is never zero for "unknown".
type SmartSummary struct {
	Protocol      string  // "ATA", "SCSI", "NVMe"
	Healthy       *bool   // smartctl's overall assessment
	PowerOnHours  *uint64 //
	TempC         *int    //
	Reallocated   *uint64 // SATA 5 | SAS grown defect list | NVMe n/a
	Pending       *uint64 // SATA 197
	Uncorrectable *uint64 // SATA 198 | SAS uncorrected read+write | NVMe media_errors
	CRCErrors     *uint64 // SATA 199
	ReadBytes     *uint64 // SAS log page | NVMe data units | SATA 242 where the raw value is sectors
	WriteBytes    *uint64 //
	PercentUsed   *uint32 // NVMe percentage_used | SAS endurance indicator | SATA 231/177 where present
	SelftestLast  string  // "short: passed @ 41120h" or ""
}

// SmartSample is one smartctl run against one device.
type SmartSample struct {
	DevName string
	At      time.Time
	Summary *SmartSummary // nil when Skipped is set
	Raw     []byte        // the JSON smartctl printed, or nil
	Skipped string        // "" | "standby" | "timeout" | "unsupported" | "error: …"
}

// Skip reasons.
const (
	SkipStandby     = "standby"
	SkipTimeout     = "timeout"
	SkipUnsupported = "unsupported"
)

// SmartRunner runs smartctl with the given arguments and returns its
// standard output and exit code. exec.CommandContext is the default; tests
// and fixtures substitute one.
type SmartRunner func(ctx context.Context, args ...string) (out []byte, exitCode int, err error)

// DefaultSmartRunner runs smartctl from PATH.
func DefaultSmartRunner(ctx context.Context, args ...string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, "smartctl", args...)
	out, err := cmd.Output()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// smartctl's exit status is a bit field and non-zero for many
		// non-fatal conditions; the JSON says what happened.
		code = exitErr.ExitCode()
		err = nil
	}
	return out, code, err
}

// smartctlTimeout bounds one run. A drive that hangs smartctl is itself a
// finding; the caller records the timeout.
const smartctlTimeout = 30 * time.Second

// SampleSmart runs smartctl against /dev/<name> and summarises the result.
// Sleeping drives are left asleep (-n standby) and reported as skipped.
// If the plain run fails to talk to the device, -d sat (USB bridges) and
// -d scsi are tried once each; the device type that worked is returned so
// the caller can remember it.
func SampleSmart(ctx context.Context, run SmartRunner, name, devType string, now time.Time) (SmartSample, string) {
	s := SmartSample{DevName: name, At: now}
	types := []string{devType}
	if devType == "" {
		types = []string{"", "sat", "scsi"}
	}
	for _, dt := range types {
		args := []string{"-j", "-a", "-n", "standby"}
		if dt != "" {
			args = append(args, "-d", dt)
		}
		args = append(args, "/dev/"+name)
		rctx, cancel := context.WithTimeout(ctx, smartctlTimeout)
		out, code, err := run(rctx, args...)
		timedOut := errors.Is(rctx.Err(), context.DeadlineExceeded)
		cancel()
		if timedOut {
			s.Skipped = SkipTimeout
			return s, dt
		}
		if err != nil {
			s.Skipped = "error: " + err.Error()
			return s, dt
		}
		var doc smartctlJSON
		if jerr := json.Unmarshal(out, &doc); jerr != nil {
			if len(out) == 0 {
				s.Skipped = "error: smartctl printed nothing"
				return s, dt
			}
			s.Skipped = "error: unparseable smartctl output"
			return s, dt
		}
		if doc.isStandby() {
			s.Skipped = SkipStandby
			return s, dt
		}
		if doc.deviceUnusable(code) {
			continue // try the next device type
		}
		s.Raw = out
		s.Summary = doc.summary()
		return s, dt
	}
	s.Skipped = SkipUnsupported
	return s, ""
}

// smartctlJSON is the subset of smartctl -j output the summary reads.
// Field names follow smartmontools 7.x.
type smartctlJSON struct {
	Smartctl struct {
		ExitStatus int `json:"exit_status"`
		Messages   []struct {
			String   string `json:"string"`
			Severity string `json:"severity"`
		} `json:"messages"`
	} `json:"smartctl"`
	Device struct {
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
	} `json:"device"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	PowerOnTime *struct {
		Hours uint64 `json:"hours"`
	} `json:"power_on_time"`
	Temperature *struct {
		Current int `json:"current"`
	} `json:"temperature"`

	// SATA
	ATAAttributes *struct {
		Table []struct {
			ID    int    `json:"id"`
			Name  string `json:"name"`
			Value int    `json:"value"`
			Raw   struct {
				Value  uint64 `json:"value"`
				String string `json:"string"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	ATASelfTestLog *struct {
		Standard struct {
			Table []struct {
				Type struct {
					String string `json:"string"`
				} `json:"type"`
				Status struct {
					String string `json:"string"`
					Passed bool   `json:"passed"`
				} `json:"status"`
				LifetimeHours uint64 `json:"lifetime_hours"`
			} `json:"table"`
		} `json:"standard"`
	} `json:"ata_smart_self_test_log"`
	LogicalBlockSize uint64 `json:"logical_block_size"`

	// SAS
	SCSIGrownDefectList *uint64 `json:"scsi_grown_defect_list"`
	SCSIErrorCounterLog *struct {
		Read  scsiErrorCounter `json:"read"`
		Write scsiErrorCounter `json:"write"`
	} `json:"scsi_error_counter_log"`
	SCSIPercentageUsed *uint32 `json:"scsi_percentage_used_endurance_indicator"`
	SCSISelfTest0      *struct {
		Code struct {
			String string `json:"string"`
		} `json:"code"`
		Result struct {
			String string `json:"string"`
		} `json:"result"`
		PowerOnTime struct {
			Hours uint64 `json:"hours"`
		} `json:"power_on_time"`
	} `json:"scsi_self_test_0"`

	// NVMe
	NVMeHealth *struct {
		CriticalWarning  int    `json:"critical_warning"`
		Temperature      int    `json:"temperature"`
		PercentageUsed   uint32 `json:"percentage_used"`
		DataUnitsRead    uint64 `json:"data_units_read"`
		DataUnitsWritten uint64 `json:"data_units_written"`
		PowerOnHours     uint64 `json:"power_on_hours"`
		MediaErrors      uint64 `json:"media_errors"`
	} `json:"nvme_smart_health_information_log"`
}

type scsiErrorCounter struct {
	TotalUncorrectedErrors uint64 `json:"total_uncorrected_errors"`
	GigabytesProcessed     string `json:"gigabytes_processed"`
}

func (d *smartctlJSON) isStandby() bool {
	for _, m := range d.Smartctl.Messages {
		if strings.Contains(m.String, "STANDBY") || strings.Contains(m.String, "standby") {
			return true
		}
	}
	return false
}

// deviceUnusable reports whether smartctl could not talk to the device at
// all (exit bit 1: command line or device open failure; bit 2: device open
// or identification failed), as opposed to a device that answered with
// problems.
func (d *smartctlJSON) deviceUnusable(code int) bool {
	if code&0x3 == 0 {
		return false
	}
	if d.SmartStatus != nil || d.ATAAttributes != nil || d.NVMeHealth != nil || d.SCSIErrorCounterLog != nil {
		return false
	}
	return true
}

func (d *smartctlJSON) summary() *SmartSummary {
	s := &SmartSummary{Protocol: d.Device.Protocol}
	if d.SmartStatus != nil {
		s.Healthy = ptr(d.SmartStatus.Passed)
	}
	if d.PowerOnTime != nil {
		s.PowerOnHours = ptr(d.PowerOnTime.Hours)
	}
	if d.Temperature != nil {
		s.TempC = ptr(d.Temperature.Current)
	}
	switch {
	case d.NVMeHealth != nil:
		h := d.NVMeHealth
		if s.PowerOnHours == nil {
			s.PowerOnHours = ptr(h.PowerOnHours)
		}
		if s.TempC == nil {
			s.TempC = ptr(h.Temperature)
		}
		s.Uncorrectable = ptr(h.MediaErrors)
		s.PercentUsed = ptr(h.PercentageUsed)
		// Data units are 1000 × 512-byte blocks.
		s.ReadBytes = ptr(h.DataUnitsRead * 512000)
		s.WriteBytes = ptr(h.DataUnitsWritten * 512000)
		if s.Healthy == nil {
			s.Healthy = ptr(h.CriticalWarning == 0)
		}
	case d.ATAAttributes != nil:
		block := d.LogicalBlockSize
		if block == 0 {
			block = 512
		}
		for _, a := range d.ATAAttributes.Table {
			switch a.ID {
			case 5:
				s.Reallocated = ptr(a.Raw.Value)
			case 197:
				s.Pending = ptr(a.Raw.Value)
			case 198:
				s.Uncorrectable = ptr(a.Raw.Value)
			case 199:
				s.CRCErrors = ptr(a.Raw.Value)
			case 241:
				if strings.HasPrefix(a.Name, "Total_LBAs_Written") || strings.Contains(a.Name, "Host_Writes") {
					s.WriteBytes = ptr(a.Raw.Value * block)
				}
			case 242:
				if strings.HasPrefix(a.Name, "Total_LBAs_Read") || strings.Contains(a.Name, "Host_Reads") {
					s.ReadBytes = ptr(a.Raw.Value * block)
				}
			case 177, 231, 233:
				if strings.Contains(a.Name, "Wear") || strings.Contains(a.Name, "SSD_Life") || strings.Contains(a.Name, "Media_Wearout") {
					// Normalised value counts down from 100.
					if a.Value >= 0 && a.Value <= 100 {
						s.PercentUsed = ptr(uint32(100 - a.Value))
					}
				}
			}
		}
		if d.ATASelfTestLog != nil && len(d.ATASelfTestLog.Standard.Table) > 0 {
			t := d.ATASelfTestLog.Standard.Table[0]
			s.SelftestLast = fmt.Sprintf("%s: %s @ %dh", strings.ToLower(firstWord(t.Type.String)), strings.ToLower(t.Status.String), t.LifetimeHours)
		}
	default: // SCSI/SAS
		if d.SCSIGrownDefectList != nil {
			s.Reallocated = ptr(*d.SCSIGrownDefectList)
		}
		if l := d.SCSIErrorCounterLog; l != nil {
			s.Uncorrectable = ptr(l.Read.TotalUncorrectedErrors + l.Write.TotalUncorrectedErrors)
			if b, ok := gigabytesToBytes(l.Read.GigabytesProcessed); ok {
				s.ReadBytes = ptr(b)
			}
			if b, ok := gigabytesToBytes(l.Write.GigabytesProcessed); ok {
				s.WriteBytes = ptr(b)
			}
		}
		if d.SCSIPercentageUsed != nil {
			s.PercentUsed = ptr(*d.SCSIPercentageUsed)
		}
		if t := d.SCSISelfTest0; t != nil && t.Code.String != "" {
			s.SelftestLast = fmt.Sprintf("%s: %s @ %dh", strings.ToLower(strings.TrimPrefix(t.Code.String, "Background ")), strings.ToLower(t.Result.String), t.PowerOnTime.Hours)
		}
	}
	return s
}

// gigabytesToBytes parses smartctl's "12345.678" gigabytes string.
func gigabytesToBytes(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return uint64(f * 1e9), true
}

func ptr[T any](v T) *T { return &v }

func firstWord(s string) string {
	w, _, _ := strings.Cut(s, " ")
	return w
}

// SmartCaptureArgs are the smartctl arguments capture records per device,
// so fixtures carry real SMART output for the parser.
func SmartCaptureArgs(name string) []string {
	return []string{"-j", "-a", "-n", "standby", "/dev/" + name}
}
