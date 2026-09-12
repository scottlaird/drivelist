package agent

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/scottlaird/drivelist/collect"
)

func TestKernelWatcherTriggersAfterSettle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		send := &fakeSender{}
		a := newAgent(t, send, inventory)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go a.Run(ctx)
		synctest.Wait()
		if send.count() != 1 {
			t.Fatalf("reports at start = %d", send.count())
		}

		w := NewKernelWatcher(a, 5*time.Second, nil)
		a.SetKernelWatcher(w)
		events := make(chan collect.KernelEvent)
		go w.Run(ctx, events)
		now := time.Now()
		events <- collect.KernelEvent{At: now, Class: collect.ClassAttach, DevName: "sdz", SenseKey: -1, ASC: -1, ASCQ: -1}
		events <- collect.KernelEvent{At: now, Class: collect.ClassDetach, DevName: "sdy", SenseKey: -1, ASC: -1, ASCQ: -1}
		synctest.Wait()
		if send.count() != 1 {
			t.Errorf("reported before the settle delay: %d", send.count())
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if send.count() != 2 {
			t.Errorf("reports after settle = %d, want 2 (two changes, one trigger)", send.count())
		}
		// Errors count but do not trigger. sda is in the inventory; sdbi is not.
		events <- collect.KernelEvent{At: now, Class: collect.ClassRecovered, DevName: "sda", SCSIAddr: "4:0:0:0", SenseKey: 1, ASC: 0xb, ASCQ: 0x97, Text: "sd 4:0:0:0: [sda] …"}
		events <- collect.KernelEvent{At: now, Class: collect.ClassRecovered, DevName: "sda", SCSIAddr: "4:0:0:0", SenseKey: 1, ASC: 0xb, ASCQ: 0x97}
		events <- collect.KernelEvent{At: now, Class: collect.ClassPredictiveFailure, DevName: "sdbi", SCSIAddr: "11:0:17:0", SenseKey: 1, ASC: 0x5d, ASCQ: 0x90}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if send.count() != 2 {
			t.Errorf("error events triggered a report: %d", send.count())
		}
		if got := send.kernelReports(); len(got) != 0 {
			t.Errorf("current hour sent early: %+v", got)
		}
		// Once the hour is over, the next report carries the completed
		// buckets: sda's (attributed), not sdbi's (unknown device).
		time.Sleep(time.Hour)
		synctest.Wait()
		kernelReports := send.kernelReports()
		if len(kernelReports) != 1 {
			t.Fatalf("kernel reports = %d, want 1", len(kernelReports))
		}
		samples := kernelReports[0].Samples
		var sdaCount uint32
		for _, s := range samples {
			if s.DevName == "sdbi" {
				t.Errorf("unknown device sent: %v", s)
			}
			if s.DevName == "sda" && s.Class == collect.ClassRecovered {
				sdaCount = s.Count
				if s.Identity.GetSerial() != "S1" || s.ScsiCode != "1:b:97" || s.Sample == "" {
					t.Errorf("sda sample = %v", s)
				}
			}
		}
		if sdaCount != 2 {
			t.Errorf("sda recovered count = %d, want 2", sdaCount)
		}
		if len(w.counts) != 0 {
			t.Errorf("buckets left after sending: %d", len(w.counts))
		}
		cancel()
	})
}
