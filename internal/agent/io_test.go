package agent

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/scottlaird/drivelist/collect"
)

func TestIOBuckets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		send := &fakeSender{}
		a := newAgent(t, send, inventory)
		// Counters advance 100 reads and 800 ms per minute; minute 10 is a
		// spike of 8000 ms. sdz is not in the inventory.
		var mu sync.Mutex
		minute := 0
		var reads, readMs uint64
		read := func() ([]collect.DiskStat, error) {
			mu.Lock()
			defer mu.Unlock()
			if minute > 0 {
				reads += 100
				if minute == 10 {
					readMs += 8000
				} else {
					readMs += 800
				}
			}
			minute++
			return []collect.DiskStat{
				{Name: "sda", Reads: reads, SectorsRead: reads * 8, ReadMs: readMs, IOMs: reads * 6},
				{Name: "sdz", Reads: reads},
			}, nil
		}
		a.EnableIO(IOConfig{Interval: time.Minute, Read: read})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// Start on a whole hour so the first bucket is clean.
		start := time.Now().Truncate(time.Hour).Add(time.Hour)
		time.Sleep(start.Sub(time.Now()))
		go a.Run(ctx)
		synctest.Wait()

		time.Sleep(59 * time.Minute)
		synctest.Wait()
		if len(send.ioReports()) != 0 {
			t.Fatal("bucket sent before the hour ended")
		}
		time.Sleep(2 * time.Minute) // the reading at :01 sees the hour done
		synctest.Wait()
		reports := send.ioReports()
		if len(reports) != 1 || len(reports[0].Samples) != 1 {
			t.Fatalf("io reports = %+v", reports)
		}
		s := reports[0].Samples[0]
		if s.DevName != "sda" || s.Identity.GetSerial() != "S1" || s.BucketStart.AsTime() != start.UTC() {
			t.Errorf("sample = %v", s)
		}
		// The readings at :01 through :59 land in this hour (the one at :00
		// of the next hour belongs to that hour): 59 deltas, 5900 reads,
		// 58×800 + 8000 ms.
		if s.Reads != 5900 || s.ReadMs != 58*800+8000 || s.ReadBytes != 5900*8*512 || s.BucketSecs != 3540 {
			t.Errorf("sums = reads %d ms %d bytes %d secs %d", s.Reads, s.ReadMs, s.ReadBytes, s.BucketSecs)
		}
		if s.RAwaitMaxMs != 80 || s.UtilMax != 0.01 {
			t.Errorf("maxima = r_await %v util %v", s.RAwaitMaxMs, s.UtilMax)
		}
		cancel()
	})
}

func TestIOKeepsBucketsWhenSendFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		send := &fakeSender{}
		a := newAgent(t, send, inventory)
		var n uint64
		read := func() ([]collect.DiskStat, error) {
			n += 100
			return []collect.DiskStat{{Name: "sda", Reads: n, ReadMs: n}}, nil
		}
		a.EnableIO(IOConfig{Interval: time.Minute, Read: read})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		time.Sleep(time.Now().Truncate(time.Hour).Add(time.Hour).Sub(time.Now()))
		go a.Run(ctx)
		synctest.Wait()
		send.setFail(true)
		time.Sleep(61 * time.Minute)
		synctest.Wait()
		if a.io.open == nil || len(a.io.open) < 1 {
			t.Fatal("bucket dropped after a failed send")
		}
		send.setFail(false)
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := send.ioReports(); len(got) != 1 {
			t.Errorf("io reports after recovery = %d, want 1", len(got))
		}
		cancel()
	})
}
