package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
	"github.com/scottlaird/drivelist/internal/pb/drivelistv1/drivelistv1connect"
	"github.com/scottlaird/drivelist/internal/store"
)

// withToken adds the bearer token to every request a client sends.
func withToken(token string) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}))
}

type testEnv struct {
	url       string
	collector drivelistv1connect.CollectorClient
	query     drivelistv1connect.QueryClient
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := New(st, Config{AgentToken: "agent-secret", OperatorToken: "op-secret", Interval: time.Minute}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return &testEnv{
		url:       hs.URL,
		collector: drivelistv1connect.NewCollectorClient(http.DefaultClient, hs.URL, withToken("agent-secret")),
		query:     drivelistv1connect.NewQueryClient(http.DefaultClient, hs.URL, withToken("op-secret")),
	}
}

func report(observed time.Time, devices ...*pb.Device) *pb.ReportInventoryRequest {
	return &pb.ReportInventoryRequest{
		Host:       &pb.HostIdentity{MachineId: "m1", Hostname: "storage1", Os: "linux"},
		ObservedAt: timestamppb.New(observed),
		Devices:    devices,
		Complete:   true,
	}
}

func device(name, serial, wwn, bay string, uses ...string) *pb.Device {
	return &pb.Device{
		DevName:   name,
		Identity:  &pb.DriveIdentity{Wwn: wwn, Model: "HUH72808", Serial: serial},
		Bus:       pb.Bus_BUS_SAS,
		SizeBytes: 8_000_000_000_000,
		Expander:  "expander-4:0",
		Bay:       bay,
		Uses:      uses,
	}
}

func TestReportAndQuery(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)

	res, err := env.collector.ReportInventory(ctx, connect.NewRequest(report(t0,
		device("sda", "X1", "0x5000000000000001", "1", "zfs > tank 1 > raidz2 10 > disk 100"),
		device("sdb", "Y1", "0x5000000000000002", "2"))))
	if err != nil {
		t.Fatalf("ReportInventory: %v", err)
	}
	if !res.Msg.Accepted || !res.Msg.Changed || len(res.Msg.Statuses) != 2 {
		t.Errorf("first report response = %v", res.Msg)
	}
	if got := res.Msg.GetConfig().GetInventoryInterval().AsDuration(); got != time.Minute {
		t.Errorf("config interval = %v, want 1m", got)
	}

	res, err = env.collector.ReportInventory(ctx, connect.NewRequest(report(t0.Add(time.Minute),
		device("sda", "X1", "0x5000000000000001", "1", "zfs > tank 1 > raidz2 10 > disk 100"),
		device("sdb", "Y1", "0x5000000000000002", "2"))))
	if err != nil || res.Msg.Changed {
		t.Errorf("heartbeat: changed=%v err=%v", res.Msg.GetChanged(), err)
	}

	hosts, err := env.query.ListHosts(ctx, connect.NewRequest(&pb.ListHostsRequest{}))
	if err != nil || len(hosts.Msg.Hosts) != 1 || hosts.Msg.Hosts[0].DriveCount != 2 {
		t.Errorf("ListHosts = %v, %v", hosts.Msg.GetHosts(), err)
	}

	d, err := env.query.GetDrive(ctx, connect.NewRequest(&pb.GetDriveRequest{Ref: "Y1"}))
	if err != nil {
		t.Fatalf("GetDrive: %v", err)
	}
	if d.Msg.Drive.Current == nil || d.Msg.Drive.Current.Bay != "2" || d.Msg.Drive.Status != "ok" || len(d.Msg.Keys) != 2 {
		t.Errorf("GetDrive(Y1) = %v", d.Msg)
	}

	unused, err := env.query.ListDrives(ctx, connect.NewRequest(&pb.ListDrivesRequest{UnusedOnly: true}))
	if err != nil || len(unused.Msg.Drives) != 1 || unused.Msg.Drives[0].Serial != "Y1" {
		t.Errorf("unused drives = %v, %v", unused.Msg.GetDrives(), err)
	}

	ann, err := env.query.Annotate(ctx, connect.NewRequest(&pb.AnnotateRequest{Ref: "Y1", Status: "bad", Note: "clicking", Actor: "scott@laptop"}))
	if err != nil || ann.Msg.Event.Kind != "status_changed" || ann.Msg.Event.Source != "user:scott@laptop" {
		t.Errorf("Annotate = %v, %v", ann.Msg.GetEvent(), err)
	}
	d, _ = env.query.GetDrive(ctx, connect.NewRequest(&pb.GetDriveRequest{Ref: "Y1"}))
	if d.Msg.Drive.Status != "bad" || d.Msg.LastStatus == nil {
		t.Errorf("after Annotate: %v", d.Msg)
	}

	// Y vanishes; it is bad, so absence is expected and missing stays empty.
	env.collector.ReportInventory(ctx, connect.NewRequest(report(t0.Add(2*time.Minute),
		device("sda", "X1", "0x5000000000000001", "1", "zfs > tank 1 > raidz2 10 > disk 100"))))
	hist, err := env.query.GetDriveHistory(ctx, connect.NewRequest(&pb.GetDriveHistoryRequest{Ref: "Y1"}))
	if err != nil || len(hist.Msg.Placements) != 1 || hist.Msg.Placements[0].EndReason != "vanished" || len(hist.Msg.Events) != 3 {
		t.Errorf("history = %v, %v", hist.Msg, err)
	}
	missing, err := env.query.ListMissing(ctx, connect.NewRequest(&pb.ListMissingRequest{}))
	if err != nil || len(missing.Msg.Drives) != 0 {
		t.Errorf("missing = %v, %v; want none (Y is bad)", missing.Msg.GetDrives(), err)
	}
	evs, err := env.query.ListEvents(ctx, connect.NewRequest(&pb.ListEventsRequest{Kinds: []string{"vanished"}}))
	if err != nil || len(evs.Msg.Events) != 1 || evs.Msg.Events[0].Serial != "Y1" {
		t.Errorf("vanished events = %v, %v", evs.Msg.GetEvents(), err)
	}
}

func TestErrorsAndAuth(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	env.collector.ReportInventory(ctx, connect.NewRequest(report(time.Now().Add(-time.Minute),
		device("sda", "X1", "0x5000000000000001", "1"), device("sdb", "X2", "0x5000000000000002", "2"))))

	_, err := env.query.GetDrive(ctx, connect.NewRequest(&pb.GetDriveRequest{Ref: "nope"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown drive: code %v, want NotFound", connect.CodeOf(err))
	}
	_, err = env.query.GetDrive(ctx, connect.NewRequest(&pb.GetDriveRequest{Ref: "X"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("ambiguous drive: code %v, want InvalidArgument (%v)", connect.CodeOf(err), err)
	}
	_, err = env.query.Annotate(ctx, connect.NewRequest(&pb.AnnotateRequest{Ref: "X1", Status: "broken"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("bad status: code %v, want InvalidArgument", connect.CodeOf(err))
	}

	wrong := drivelistv1connect.NewQueryClient(http.DefaultClient, env.url, withToken("agent-secret"))
	_, err = wrong.ListHosts(ctx, connect.NewRequest(&pb.ListHostsRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("agent token on Query: code %v, want Unauthenticated", connect.CodeOf(err))
	}
	none := drivelistv1connect.NewCollectorClient(http.DefaultClient, env.url)
	_, err = none.ReportInventory(ctx, connect.NewRequest(report(time.Now())))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("no token on Collector: code %v, want Unauthenticated", connect.CodeOf(err))
	}

	if _, err := New(nil, Config{AgentToken: "x"}, nil); err == nil {
		t.Error("New without an operator token succeeded")
	}
	var ce *connect.Error
	if !errors.As(err, &ce) && err != nil {
		t.Logf("last error was not a connect error: %v", err)
	}
}

func TestKernelRoundTrip(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	hour := time.Now().Add(-2 * time.Hour).Truncate(time.Hour)
	env.collector.ReportInventory(ctx, connect.NewRequest(report(hour, device("sda", "X1", "0x5000000000000001", "1"))))
	ack, err := env.collector.ReportKernel(ctx, connect.NewRequest(&pb.ReportKernelRequest{
		Host: &pb.HostIdentity{MachineId: "m1", Hostname: "storage1"},
		Samples: []*pb.KernelSample{{
			Identity: &pb.DriveIdentity{Wwn: "0x5000000000000001"}, DevName: "sda", BucketStart: timestamppb.New(hour), BucketSecs: 3600,
			Class: "predictive_failure", ScsiCode: "1:5d:90", Count: 3, Sample: "sd 4:0:0:0: [sda] tag#1 ASC=0x5d",
		}},
	}))
	if err != nil || ack.Msg.Stored != 1 {
		t.Fatalf("ReportKernel = %v, %v", ack.Msg, err)
	}
	res, err := env.query.GetKernel(ctx, connect.NewRequest(&pb.GetKernelRequest{Ref: "X1"}))
	if err != nil || len(res.Msg.Samples) != 1 || res.Msg.Samples[0].Hostname != "storage1" || res.Msg.Samples[0].Count != 3 {
		t.Errorf("GetKernel = %v, %v", res.Msg, err)
	}
	evs, _ := env.query.ListEvents(ctx, connect.NewRequest(&pb.ListEventsRequest{Kinds: []string{"kernel_warning"}}))
	if len(evs.Msg.Events) != 1 || evs.Msg.Events[0].Serial != "X1" {
		t.Errorf("kernel_warning events = %v", evs.Msg.GetEvents())
	}
}

func TestMetrics(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	env.collector.ReportInventory(ctx, connect.NewRequest(report(time.Now().Add(-time.Minute),
		device("sda", "X1", "0x5000000000000001", "1"), device("sdb", "Y1", "0x5000000000000002", "2"))))
	env.query.Annotate(ctx, connect.NewRequest(&pb.AnnotateRequest{Ref: "Y1", Status: "bad", Actor: "t"}))
	resp, err := http.Get(env.url + "/metrics")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("/metrics: %v %v", resp, err)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		`drivelist_host_drives{host="storage1"} 2`,
		`drivelist_host_missing{host="storage1"} 0`,
		`drivelist_host_stale{host="storage1"} 0`,
		`drivelist_drives{status="bad"} 1`,
		`drivelist_drives{status="ok"} 1`,
		`drivelist_reports_total{host="storage1",outcome="changed"} 1`,
		`drivelist_kernel_warnings_24h 0`,
		`drivelist_scrape_error 0`,
		`go_goroutines`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
}

func TestHealthz(t *testing.T) {
	env := newEnv(t)
	resp, err := http.Get(env.url + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("/healthz: %v %v", resp, err)
	}
}
