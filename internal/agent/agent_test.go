package agent

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/scottlaird/drivelist"
	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
)

// fakeSender records reports and can be told to fail.
type fakeSender struct {
	mu       sync.Mutex
	reports  []*pb.ReportInventoryRequest
	kernel   []*pb.ReportKernelRequest
	fail     bool
	interval time.Duration
	reject   string
}

func (f *fakeSender) Report(_ context.Context, req *pb.ReportInventoryRequest) (*pb.ReportInventoryResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("connection refused")
	}
	f.reports = append(f.reports, req)
	res := &pb.ReportInventoryResponse{Accepted: f.reject == "", RejectReason: f.reject, Changed: len(f.reports) == 1}
	if f.interval > 0 {
		res.Config = &pb.AgentConfig{InventoryInterval: durationpb.New(f.interval)}
	}
	res.Statuses = []*pb.DriveStatus{{Identity: &pb.DriveIdentity{Wwn: "0x1", Model: "M", Serial: "S1"}, Status: "bad", Note: "clicking"}}
	return res, nil
}

func (f *fakeSender) Kernel(_ context.Context, req *pb.ReportKernelRequest) (*pb.ReportAck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("connection refused")
	}
	f.kernel = append(f.kernel, req)
	return &pb.ReportAck{Accepted: true, Stored: uint32(len(req.Samples))}, nil
}

func (f *fakeSender) kernelReports() []*pb.ReportKernelRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pb.ReportKernelRequest(nil), f.kernel...)
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reports)
}

func (f *fakeSender) setFail(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = v
}

func (f *fakeSender) setReject(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reject = reason
}

var host = &pb.HostIdentity{MachineId: "m", Hostname: "h"}

func inventory() (*drivelist.Inventory, error) {
	inv := drivelist.NewInventory()
	inv.Add(&drivelist.Device{DeviceName: "sda", WWN: "0x1", Model: "M", Serial: "S1", Devices: []string{"/dev/sda"}, Attribs: map[string]string{}})
	return inv, nil
}

func newAgent(t *testing.T, send Sender, collect func() (*drivelist.Inventory, error)) *Agent {
	t.Helper()
	dir := t.TempDir()
	a, err := New(Config{Host: host, Interval: 5 * time.Minute, SpoolDir: filepath.Join(dir, "spool"), StatusPath: filepath.Join(dir, "status.json"), MaxSpool: 3}, send, collect, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRunReportsOnScheduleAndTrigger(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		send := &fakeSender{}
		a := newAgent(t, send, inventory)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go a.Run(ctx)
		synctest.Wait()
		if got := send.count(); got != 1 {
			t.Fatalf("reports at start = %d, want 1", got)
		}
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if got := send.count(); got != 2 {
			t.Fatalf("reports after one interval = %d, want 2", got)
		}
		a.Trigger("attach")
		synctest.Wait()
		if got := send.count(); got != 3 {
			t.Fatalf("reports after trigger = %d, want 3", got)
		}
		// The trigger reset the timer: the next tick is a full interval later.
		time.Sleep(4 * time.Minute)
		synctest.Wait()
		if got := send.count(); got != 3 {
			t.Errorf("reports 4m after trigger = %d, want 3", got)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := send.count(); got != 4 {
			t.Errorf("reports 5m after trigger = %d, want 4", got)
		}

		c, err := ReadStatusCache(a.cfg.StatusPath)
		if err != nil {
			t.Fatal(err)
		}
		if st, note := c.Lookup("0x1", "", ""); st != "bad" || note != "clicking" {
			t.Errorf("status cache lookup = %q %q", st, note)
		}
		if st, _ := c.Lookup("", "M", "S1"); st != "bad" {
			t.Errorf("lookup by model/serial = %q", st)
		}
		if st, _ := c.Lookup("0x2", "", ""); st != "" {
			t.Errorf("lookup of unknown drive = %q", st)
		}
		cancel()
	})
}

func TestTriggerCoalesces(t *testing.T) {
	a := newAgent(t, &fakeSender{}, inventory)
	for range 3 {
		a.Trigger("attach") // must not block with nobody listening
	}
	if got := len(a.trigger); got != 1 {
		t.Errorf("pending triggers = %d, want 1", got)
	}
}

func TestServerSetsInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		send := &fakeSender{interval: time.Minute}
		a := newAgent(t, send, inventory)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go a.Run(ctx)
		synctest.Wait()
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		if got := send.count(); got != 4 {
			t.Errorf("reports after 3m at a 1m server interval = %d, want 4", got)
		}
		cancel()
	})
}

func TestSpoolAndReplay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		send := &fakeSender{fail: true}
		var mu sync.Mutex
		uses := []string{}
		setUses := func(u ...string) {
			mu.Lock()
			defer mu.Unlock()
			uses = u
		}
		a := newAgent(t, send, func() (*drivelist.Inventory, error) {
			inv, _ := inventory()
			mu.Lock()
			defer mu.Unlock()
			inv.Devices[0].Uses = uses
			return inv, nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go a.Run(ctx)
		synctest.Wait()
		time.Sleep(10 * time.Minute) // three failed reports, identical content
		synctest.Wait()
		if got := a.spool.len(); got != 1 {
			t.Fatalf("spool after three identical failures = %d entries, want 1 (collapsed)", got)
		}

		// Content changes while still down: a second entry.
		setUses("mount > /x")
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if got := a.spool.len(); got != 2 {
			t.Fatalf("spool after a changed report = %d, want 2", got)
		}
		// Beyond the cap the oldest goes.
		for i := range 3 {
			setUses("mount > /y", string(rune('a'+i)))
			time.Sleep(5 * time.Minute)
			synctest.Wait()
		}
		if got := a.spool.len(); got != 3 {
			t.Fatalf("spool past the cap = %d, want 3", got)
		}

		// Server back: the spool drains oldest first, then the live report.
		send.setFail(false)
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if got := send.count(); got != 4 {
			t.Fatalf("reports after recovery = %d, want 3 spooled + 1 live", got)
		}
		if a.spool.len() != 0 {
			t.Errorf("spool not empty after replay: %d", a.spool.len())
		}
		for i := 1; i < len(send.reports); i++ {
			if !send.reports[i].ObservedAt.AsTime().After(send.reports[i-1].ObservedAt.AsTime()) {
				t.Errorf("replay out of order at %d", i)
			}
		}
		cancel()
	})
}

func TestRejectedSpoolEntryIsDropped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		send := &fakeSender{fail: true}
		a := newAgent(t, send, inventory)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go a.Run(ctx)
		synctest.Wait()
		send.setFail(false)
		send.setReject("stale")
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if a.spool.len() != 0 {
			t.Errorf("rejected spooled report kept: %d entries", a.spool.len())
		}
		cancel()
	})
}

func TestCollectFailureReportsIncomplete(t *testing.T) {
	send := &fakeSender{}
	a := newAgent(t, send, func() (*drivelist.Inventory, error) { return nil, errors.New("zpool exploded") })
	interval := a.cfg.Interval
	a.cycle(context.Background(), "test", &interval)
	if got := send.count(); got != 1 || send.reports[0].Complete || len(send.reports[0].CollectorErrors) != 1 {
		t.Errorf("incomplete report = %v", send.reports)
	}
}

func TestNewRequiresHost(t *testing.T) {
	if _, err := New(Config{}, &fakeSender{}, inventory, nil); err == nil {
		t.Error("New without a host succeeded")
	}
}
