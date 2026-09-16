package collect

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// Lines from fs2's dmesg and a few other kernels, with what each should
// classify as. Two-line SCSI sense output is fed as consecutive lines.
func TestClassify(t *testing.T) {
	now := time.Date(2026, 9, 12, 22, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		lines []string
		want  []KernelEvent // Text left empty means "do not check"
	}{
		{"predictive failure via ASC 0x5d on a recovered error", []string{
			"scsi target11:0:17: enclosure logical id(0x5000ccab020094ff), slot(54) ",
			"sd 11:0:17:0: [sdag] tag#784 Sense Key : Recovered Error [current] [descriptor] ",
			"sd 11:0:17:0: [sdag] tag#784 ASC=0x5d <<vendor>>ASCQ=0x90 ",
		}, []KernelEvent{{Class: ClassPredictiveFailure, DevName: "sdag", SCSIAddr: "11:0:17:0", SenseKey: 1, ASC: 0x5d, ASCQ: 0x90}}},
		{"vendor warning is just recovered", []string{
			"sd 11:0:45:0: [sdbi] tag#2209 Sense Key : Recovered Error [current] [descriptor] ",
			"sd 11:0:45:0: [sdbi] tag#2209 ASC=0xb <<vendor>>ASCQ=0x97 ",
		}, []KernelEvent{{Class: ClassRecovered, DevName: "sdbi", SCSIAddr: "11:0:45:0", SenseKey: 1, ASC: 0xb, ASCQ: 0x97}}},
		{"interleaved tags pair correctly", []string{
			"sd 11:0:45:0: [sdbi] tag#1 Sense Key : Medium Error [current] ",
			"sd 11:0:45:0: [sdbi] tag#2 Sense Key : Recovered Error [current] ",
			"sd 11:0:45:0: [sdbi] tag#2 ASC=0x0b ASCQ=0x00 ",
			"sd 11:0:45:0: [sdbi] tag#1 ASC=0x11 ASCQ=0x00 ",
		}, []KernelEvent{
			{Class: ClassRecovered, DevName: "sdbi", SCSIAddr: "11:0:45:0", SenseKey: 1, ASC: 0xb, ASCQ: 0},
			{Class: ClassMediumError, DevName: "sdbi", SCSIAddr: "11:0:45:0", SenseKey: 3, ASC: 0x11, ASCQ: 0},
		}},
		{"hardware error", []string{
			"sd 4:0:0:0: [sda] tag#9 Sense Key : Hardware Error [current] ",
			"sd 4:0:0:0: [sda] tag#9 ASC=0x44 ASCQ=0x00 ",
		}, []KernelEvent{{Class: ClassHardwareError, DevName: "sda", SCSIAddr: "4:0:0:0", SenseKey: 4, ASC: 0x44, ASCQ: 0}}},
		{"attach and detach", []string{
			"[496866.031804] sd 11:0:17:0: [sdag] Attached SCSI disk",
			"sd 11:0:17:0: [sdag] Synchronizing SCSI cache",
			"mpt3sas_cm0: removing handle(0x0029), sas_addr(0x5000cca25492cd81)",
			"sd 11:0:17:0: [sdag] rejecting I/O to offline device",
		}, []KernelEvent{
			{Class: ClassAttach, DevName: "sdag", SCSIAddr: "11:0:17:0", SenseKey: -1, ASC: -1, ASCQ: -1},
			{Class: ClassDetach, DevName: "sdag", SCSIAddr: "11:0:17:0", SenseKey: -1, ASC: -1, ASCQ: -1},
			{Class: ClassDetach, SenseKey: -1, ASC: -1, ASCQ: -1},
			{Class: ClassDetach, DevName: "sdag", SCSIAddr: "11:0:17:0", SenseKey: -1, ASC: -1, ASCQ: -1},
		}},
		{"memory and machine-check errors about the host", []string{
			"[Hardware Error]: Unified Memory Controller Ext. Error Code: 0",
			"EDAC MC0: 1 CE on mc#0csrow#3channel#1 (csrow:3 channel:1 page:0x117fcc offset:0xb40 grain:64 syndrome:0xc00)",
			"[Hardware Error]: cache level: L3/GEN, tx: GEN, mem-tx: RD",
			"mce: [Hardware Error]: Machine check events logged",
			"[Hardware Error]: Corrected error, no action required.",
			"[Hardware Error]: CPU:0 (1a:44:0) MC22_STATUS[Over|CE|MiscV|AddrV|-|-|SyndV|CECC|-|-|-]: 0xdc2040000400011b",
			"EDAC MC1: 2 UE on DIMM_B2 (csrow:1 channel:0 page:0x0 offset:0x0 grain:64)",
			"{1}[Hardware Error]: event severity: fatal",
			"mce: [Hardware Error]: CPU 3: Machine Check Exception: 5 Bank 7: be00000000800400",
		}, []KernelEvent{
			{Class: ClassHWCorrected, Location: "mc0/csrow3/ch1", SenseKey: -1, ASC: -1, ASCQ: -1},
			{Class: ClassHWCorrected, SenseKey: -1, ASC: -1, ASCQ: -1},
			{Class: ClassHWUncorrected, Location: "mc1/DIMM_B2", SenseKey: -1, ASC: -1, ASCQ: -1},
			{Class: ClassHWUncorrected, SenseKey: -1, ASC: -1, ASCQ: -1},
			{Class: ClassHWUncorrected, SenseKey: -1, ASC: -1, ASCQ: -1},
		}},
		{"block layer", []string{
			"blk_update_request: I/O error, dev sdc, sector 123456 op 0x0:(READ) flags 0x0 phys_seg 1 prio class 0",
			"critical medium error, dev sdc, sector 8 op 0x0:(READ) flags 0x80700 phys_seg 1 prio class 2",
			"Buffer I/O error on dev sdc1, logical block 0, async page read",
		}, []KernelEvent{
			{Class: ClassIOError, DevName: "sdc", SenseKey: -1, ASC: -1, ASCQ: -1},
			{Class: ClassMediumError, DevName: "sdc", SenseKey: -1, ASC: -1, ASCQ: -1},
			{Class: ClassIOError, DevName: "sdc", SenseKey: -1, ASC: -1, ASCQ: -1},
		}},
		{"ata", []string{
			"ata3.00: exception Emask 0x0 SAct 0x40000000 SErr 0x0 action 0x0",
			"ata3.00: failed command: READ FPDMA QUEUED",
			"ata3.00: error: { UNC }",
			"ata3: hard resetting link",
			"ata3.00: ATA-9: ST8000DM004-2CX188, 0001, max UDMA/133",
		}, []KernelEvent{
			{Class: ClassTimeout, ATAPort: "ata3.00", SenseKey: -1, ASC: -1, ASCQ: -1},
			{Class: ClassTimeout, ATAPort: "ata3.00", SenseKey: -1, ASC: -1, ASCQ: -1},
			{Class: ClassMediumError, ATAPort: "ata3.00", SenseKey: -1, ASC: -1, ASCQ: -1},
			{Class: ClassLinkReset, ATAPort: "ata3", SenseKey: -1, ASC: -1, ASCQ: -1},
		}},
		{"nvme", []string{
			"nvme nvme0: I/O 12 QID 3 timeout, reset controller",
			"nvme0n1: I/O Cmd(0x2) @ LBA 1234, 8 blocks, I/O Error (sct 0x2 / sc 0x81) MORE",
		}, []KernelEvent{
			{Class: ClassTimeout, DevName: "nvme0", SenseKey: -1, ASC: -1, ASCQ: -1},
			{Class: ClassIOError, DevName: "nvme0n1", SenseKey: -1, ASC: -1, ASCQ: -1},
		}},
		{"unknown line naming a device", []string{
			"EXT4-fs (sdb1): mounted filesystem with ordered data mode",
			"usb 1-1: new high-speed USB device number 3",
		}, []KernelEvent{{Class: ClassOther, DevName: "sdb", SenseKey: -1, ASC: -1, ASCQ: -1}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newClassifier()
			var got []KernelEvent
			for _, line := range tc.lines {
				if ev, ok := c.classify(now, stripTimestamp(line)); ok {
					ev.Text = ""
					ev.At = time.Time{}
					got = append(got, ev)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d events, want %d:\n%+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("event %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestCode(t *testing.T) {
	if got := (KernelEvent{SenseKey: 1, ASC: 0x5d, ASCQ: 0x90}).Code(); got != "1:5d:90" {
		t.Errorf("Code() = %q", got)
	}
	if got := (KernelEvent{SenseKey: -1, ASC: -1, ASCQ: -1}).Code(); got != "" {
		t.Errorf("Code() without sense = %q", got)
	}
}

func TestFollowKernelLog(t *testing.T) {
	pr, pw := io.Pipe()
	out := make(chan KernelEvent, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	now := func() time.Time { return time.Date(2026, 9, 12, 22, 0, 0, 0, time.UTC) }
	go func() { errc <- FollowKernelLog(ctx, pr, now, out) }()

	// Real kmsg records: header, then message; a continuation line; noise.
	io.WriteString(pw, "6,1234,496866031804,-;sd 10:0:4:0: [sdg] Attached SCSI disk\n")
	io.WriteString(pw, " SUBSYSTEM=scsi\n")
	io.WriteString(pw, "6,1235,496866031900,-;random: crng init done\n")
	io.WriteString(pw, "4,1236,496866032000,-;sd 10:0:4:0: [sdg] tag#1789 Sense Key : Recovered Error [current] [descriptor] \n")
	io.WriteString(pw, "4,1237,496866032001,-;sd 10:0:4:0: [sdg] tag#1789 ASC=0xb <<vendor>>ASCQ=0x97 \n")

	got := []KernelEvent{<-out, <-out}
	if got[0].Class != ClassAttach || got[0].DevName != "sdg" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Class != ClassRecovered || got[1].Code() != "1:b:97" || !strings.Contains(got[1].Text, " | ") {
		t.Errorf("second = %+v", got[1])
	}
	cancel()
	pw.Close()
	if err := <-errc; err != nil {
		t.Errorf("FollowKernelLog after cancel = %v", err)
	}
}
