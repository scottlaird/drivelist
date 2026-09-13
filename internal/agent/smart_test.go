package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/scottlaird/drivelist"
)

// fixtureSmart serves the SATA fixture for any device; tests vary it
// through the returned setter.
func fixtureSmart(t *testing.T) (run func(context.Context, ...string) ([]byte, int, error), setFile func(string)) {
	t.Helper()
	var mu sync.Mutex
	name := "sata"
	setFile = func(n string) {
		mu.Lock()
		defer mu.Unlock()
		name = n
	}
	run = func(context.Context, ...string) ([]byte, int, error) {
		mu.Lock()
		file := name
		mu.Unlock()
		b, err := os.ReadFile(filepath.Join("..", "..", "collect", "testdata", "smart", file+".json"))
		if err != nil {
			t.Fatal(err)
		}
		return b, 0, nil
	}
	return run, setFile
}

func TestSmartPasses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		send := &fakeSender{}
		a := newAgent(t, send, inventory)
		run, setFile := fixtureSmart(t)
		a.EnableSmart(SmartConfig{Interval: 6 * time.Hour, Run: run, Settle: 10 * time.Second})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go a.Run(ctx)
		synctest.Wait()

		// Baseline pass right after the first report, with the raw document.
		reports := send.smartReports()
		if len(reports) != 1 || len(reports[0].Samples) != 1 {
			t.Fatalf("baseline smart reports = %+v", reports)
		}
		s := reports[0].Samples[0]
		if s.DevName != "sda" || s.Identity.GetSerial() != "S1" || s.Summary.GetPowerOnHours() != 12345 || len(s.RawJsonGz) == 0 {
			t.Errorf("baseline sample = %v", s)
		}

		// Six hours on: another pass, unchanged summary, no raw.
		time.Sleep(6 * time.Hour)
		synctest.Wait()
		reports = send.smartReports()
		if len(reports) != 2 || len(reports[1].Samples[0].RawJsonGz) != 0 {
			t.Fatalf("second pass = %+v", reports)
		}
		// A day on (four passes later), raw again even though nothing changed.
		time.Sleep(18 * time.Hour)
		synctest.Wait()
		reports = send.smartReports()
		if len(reports) != 5 || len(reports[4].Samples[0].RawJsonGz) == 0 {
			t.Fatalf("daily raw: %d reports, last raw %d bytes", len(reports), len(reports[len(reports)-1].Samples[0].RawJsonGz))
		}

		// The summary changes (fixture swap): raw straight away on the next pass.
		setFile("sas")
		time.Sleep(6 * time.Hour)
		synctest.Wait()
		reports = send.smartReports()
		if last := reports[len(reports)-1].Samples[0]; len(last.RawJsonGz) == 0 || last.Summary.GetProtocol() != "SCSI" {
			t.Errorf("changed summary did not carry raw: %v", last)
		}

		// A request samples just that device after the settle delay.
		before := len(send.smartReports())
		a.RequestSmart("sda")
		a.RequestSmart("sda")
		synctest.Wait()
		if len(send.smartReports()) != before {
			t.Error("request sampled before the settle delay")
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if got := len(send.smartReports()); got != before+1 {
			t.Errorf("smart reports after request = %d, want %d", got, before+1)
		}
		cancel()
	})
}

func TestSmartRequestOnNewDevice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		send := &fakeSender{}
		var mu sync.Mutex
		extra := false
		a := newAgent(t, send, func() (*drivelist.Inventory, error) {
			inv, _ := inventory()
			mu.Lock()
			defer mu.Unlock()
			if extra {
				inv.Add(&drivelist.Device{DeviceName: "sdb", WWN: "0x2", Model: "M", Serial: "S2", Devices: []string{"/dev/sdb"}, Attribs: map[string]string{}})
			}
			return inv, nil
		})
		run, _ := fixtureSmart(t)
		a.EnableSmart(SmartConfig{Interval: 6 * time.Hour, Run: run, Settle: 10 * time.Second})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go a.Run(ctx)
		synctest.Wait()
		mu.Lock()
		extra = true
		mu.Unlock()
		a.Trigger("attach sdb")
		synctest.Wait()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		reports := send.smartReports()
		last := reports[len(reports)-1]
		if len(reports) != 2 || len(last.Samples) != 1 || last.Samples[0].DevName != "sdb" {
			t.Errorf("new device pass = %+v", reports)
		}
		cancel()
	})
}
