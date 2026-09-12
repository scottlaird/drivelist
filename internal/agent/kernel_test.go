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
		// Errors count but do not trigger.
		events <- collect.KernelEvent{At: now, Class: collect.ClassRecovered, DevName: "sdbi", SCSIAddr: "11:0:45:0", SenseKey: 1, ASC: 0xb, ASCQ: 0x97, Text: "sd 11:0:45:0: [sdbi] …"}
		events <- collect.KernelEvent{At: now, Class: collect.ClassRecovered, DevName: "sdbi", SCSIAddr: "11:0:45:0", SenseKey: 1, ASC: 0xb, ASCQ: 0x97}
		events <- collect.KernelEvent{At: now, Class: collect.ClassPredictiveFailure, DevName: "sdag", SCSIAddr: "11:0:17:0", SenseKey: 1, ASC: 0x5d, ASCQ: 0x90}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if send.count() != 2 {
			t.Errorf("error events triggered a report: %d", send.count())
		}
		if got := w.Drain(now); len(got) != 0 {
			t.Errorf("current hour drained early: %+v", got)
		}
		got := w.Drain(now.Add(time.Hour))
		if len(got) != 4 { // attach, detach, recovered x2 (one bucket), predictive
			t.Fatalf("drained %d buckets, want 4: %+v", len(got), got)
		}
		for _, c := range got {
			if c.DevName == "sdbi" && (c.Count != 2 || c.Code != "1:b:97" || c.Sample == "") {
				t.Errorf("sdbi bucket = %+v", c)
			}
		}
		if len(w.counts) != 0 {
			t.Errorf("buckets left after drain: %d", len(w.counts))
		}
		cancel()
	})
}
