// Package server serves the drivelist.v1 API over connect: the Collector
// service agents report to and the Query service the CLI reads from, on one
// http.Handler, each behind its own bearer token.
package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
	"github.com/scottlaird/drivelist/internal/pb/drivelistv1/drivelistv1connect"
	dlversion "github.com/scottlaird/drivelist/internal/report"
	"github.com/scottlaird/drivelist/internal/store"
)

// Config is what a Server needs beyond its store.
type Config struct {
	// AgentToken and OperatorToken are the bearer tokens for the two
	// services. Both are required.
	AgentToken    string
	OperatorToken string
	// Interval is how often agents are expected to report; it is sent to
	// them and drives the stale-host sweeper.
	Interval time.Duration
	// RetainIO is how long hourly I/O buckets are kept before being rolled
	// into daily rows. 0 means 180 days.
	RetainIO time.Duration
}

// Server implements both services.
type Server struct {
	store   *store.Store
	cfg     Config
	log     *slog.Logger
	metrics *metrics
}

// New returns a server over st. It fails rather than run without tokens.
func New(st *store.Store, cfg Config, log *slog.Logger) (*Server, error) {
	if cfg.AgentToken == "" || cfg.OperatorToken == "" {
		return nil, errors.New("both the agent token and the operator token are required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Minute
	}
	if cfg.RetainIO <= 0 {
		cfg.RetainIO = 180 * 24 * time.Hour
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{store: st, cfg: cfg, log: log, metrics: newMetrics(st)}, nil
}

// Handler mounts both services, /healthz, and /metrics. Metrics carry
// hostnames and counts only and are served without a token, as Prometheus
// scrapers expect.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(drivelistv1connect.NewCollectorHandler(s, connect.WithInterceptors(bearerAuth(s.cfg.AgentToken))))
	mux.Handle(drivelistv1connect.NewQueryHandler(s, connect.WithInterceptors(bearerAuth(s.cfg.OperatorToken))))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "ok drivelist %s\n", dlversion.Version) })
	mux.Handle("/metrics", s.metrics.handler())
	return mux
}

// RunSweeper marks stale hosts once per interval, and once a day rolls
// hourly I/O older than RetainIO into daily rows, until ctx ends.
func (s *Server) RunSweeper(ctx context.Context) {
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	retain := time.NewTicker(24 * time.Hour)
	defer retain.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.store.Sweep(ctx, s.cfg.Interval)
			if err != nil {
				s.log.Error("sweep", "err", err)
			} else if n > 0 {
				s.log.Warn("hosts went stale", "count", n)
			}
		case <-retain.C:
			n, err := s.store.RetainIO(ctx, s.cfg.RetainIO)
			if err != nil {
				s.log.Error("io retention", "err", err)
			} else if n > 0 {
				s.log.Info("io retention", "hourly_rows_rolled_up", n)
			}
		}
	}
}

// bearerAuth rejects requests whose Authorization header is not
// "Bearer <token>". Comparison is constant-time.
func bearerAuth(token string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			got := strings.TrimPrefix(req.Header().Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing or wrong bearer token"))
			}
			return next(ctx, req)
		}
	}
}

// ---------- Collector ----------

func (s *Server) ReportInventory(ctx context.Context, req *connect.Request[pb.ReportInventoryRequest]) (*connect.Response[pb.ReportInventoryResponse], error) {
	r := reportFromProto(req.Msg)
	start := time.Now()
	res, err := s.store.Ingest(ctx, r)
	if err != nil {
		s.metrics.observeReport(r.Host.Hostname, "error", time.Since(start))
		s.log.Error("ingest", "host", r.Host.Hostname, "err", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	outcome := "heartbeat"
	switch {
	case !res.Accepted:
		outcome = "rejected"
		s.log.Warn("report rejected", "host", r.Host.Hostname, "reason", res.RejectReason)
	case res.Changed:
		outcome = "changed"
		s.log.Info("inventory changed", "host", r.Host.Hostname, "devices", len(r.Devices))
	}
	s.metrics.observeReport(r.Host.Hostname, outcome, time.Since(start))
	out := &pb.ReportInventoryResponse{
		Accepted:     res.Accepted,
		RejectReason: res.RejectReason,
		Changed:      res.Changed,
		Config:       &pb.AgentConfig{InventoryInterval: durationpb.New(s.cfg.Interval)},
	}
	for _, st := range res.Statuses {
		out.Statuses = append(out.Statuses, &pb.DriveStatus{Identity: identityToProto(st.Identity), Status: st.Status, Note: st.Note})
	}
	return connect.NewResponse(out), nil
}

func (s *Server) ReportKernel(ctx context.Context, req *connect.Request[pb.ReportKernelRequest]) (*connect.Response[pb.ReportAck], error) {
	host := hostIdentityFromProto(req.Msg.GetHost())
	n, err := s.store.IngestKernel(ctx, host, kernelSamplesFromProto(req.Msg.GetSamples()))
	if err != nil {
		s.log.Error("ingest kernel", "host", host.Hostname, "err", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if n > 0 {
		s.log.Info("kernel counts stored", "host", host.Hostname, "buckets", n, "received", len(req.Msg.GetSamples()))
	}
	return connect.NewResponse(&pb.ReportAck{Accepted: true, Stored: uint32(n)}), nil
}

func (s *Server) ReportSmart(ctx context.Context, req *connect.Request[pb.ReportSmartRequest]) (*connect.Response[pb.ReportAck], error) {
	host := hostIdentityFromProto(req.Msg.GetHost())
	n, err := s.store.IngestSmart(ctx, host, smartSamplesFromProto(req.Msg.GetSamples()))
	if err != nil {
		s.log.Error("ingest smart", "host", host.Hostname, "err", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.Info("smart samples stored", "host", host.Hostname, "stored", n, "received", len(req.Msg.GetSamples()))
	return connect.NewResponse(&pb.ReportAck{Accepted: true, Stored: uint32(n)}), nil
}

func (s *Server) ReportIO(ctx context.Context, req *connect.Request[pb.ReportIORequest]) (*connect.Response[pb.ReportAck], error) {
	host := hostIdentityFromProto(req.Msg.GetHost())
	n, err := s.store.IngestIO(ctx, host, ioSamplesFromProto(req.Msg.GetSamples()))
	if err != nil {
		s.log.Error("ingest io", "host", host.Hostname, "err", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.Debug("io buckets stored", "host", host.Hostname, "stored", n, "received", len(req.Msg.GetSamples()))
	return connect.NewResponse(&pb.ReportAck{Accepted: true, Stored: uint32(n)}), nil
}

// ---------- Query ----------

func (s *Server) ListHosts(ctx context.Context, _ *connect.Request[pb.ListHostsRequest]) (*connect.Response[pb.ListHostsResponse], error) {
	hosts, err := s.store.ListHosts(ctx)
	if err != nil {
		return nil, storeErr(err)
	}
	out := &pb.ListHostsResponse{}
	for _, h := range hosts {
		out.Hosts = append(out.Hosts, hostToProto(h))
	}
	return connect.NewResponse(out), nil
}

func (s *Server) ListDrives(ctx context.Context, req *connect.Request[pb.ListDrivesRequest]) (*connect.Response[pb.ListDrivesResponse], error) {
	drives, err := s.store.ListDrives(ctx, store.DriveFilter{
		Host:        req.Msg.GetHost(),
		Status:      req.Msg.GetStatus(),
		UnusedOnly:  req.Msg.GetUnusedOnly(),
		MissingOnly: req.Msg.GetMissingOnly(),
		Model:       req.Msg.GetModel(),
	})
	if err != nil {
		return nil, storeErr(err)
	}
	out := &pb.ListDrivesResponse{}
	for _, d := range drives {
		out.Drives = append(out.Drives, driveToProto(d))
	}
	return connect.NewResponse(out), nil
}

func (s *Server) GetDrive(ctx context.Context, req *connect.Request[pb.GetDriveRequest]) (*connect.Response[pb.GetDriveResponse], error) {
	d, keys, last, err := s.store.GetDrive(ctx, req.Msg.GetRef())
	if err != nil {
		return nil, storeErr(err)
	}
	out := &pb.GetDriveResponse{Drive: driveToProto(d), Keys: keys}
	if last != nil {
		out.LastStatus = eventToProto(*last)
	}
	return connect.NewResponse(out), nil
}

func (s *Server) GetDriveHistory(ctx context.Context, req *connect.Request[pb.GetDriveHistoryRequest]) (*connect.Response[pb.GetDriveHistoryResponse], error) {
	d, ps, evs, err := s.store.DriveHistory(ctx, req.Msg.GetRef())
	if err != nil {
		return nil, storeErr(err)
	}
	out := &pb.GetDriveHistoryResponse{Drive: driveToProto(d)}
	for i := range ps {
		out.Placements = append(out.Placements, placementToProto(&ps[i]))
	}
	for _, e := range evs {
		out.Events = append(out.Events, eventToProto(e))
	}
	return connect.NewResponse(out), nil
}

func (s *Server) ListEvents(ctx context.Context, req *connect.Request[pb.ListEventsRequest]) (*connect.Response[pb.ListEventsResponse], error) {
	f := store.EventFilter{Kinds: req.Msg.GetKinds(), Host: req.Msg.GetHost(), Limit: int(req.Msg.GetLimit())}
	if since := req.Msg.GetSince(); since != nil {
		f.Since = since.AsTime()
	}
	evs, err := s.store.ListEvents(ctx, f)
	if err != nil {
		return nil, storeErr(err)
	}
	out := &pb.ListEventsResponse{}
	for _, e := range evs {
		out.Events = append(out.Events, eventToProto(e))
	}
	return connect.NewResponse(out), nil
}

func (s *Server) ListMissing(ctx context.Context, _ *connect.Request[pb.ListMissingRequest]) (*connect.Response[pb.ListMissingResponse], error) {
	drives, ghosts, err := s.store.ListMissing(ctx)
	if err != nil {
		return nil, storeErr(err)
	}
	out := &pb.ListMissingResponse{}
	for _, d := range drives {
		out.Drives = append(out.Drives, driveToProto(d))
	}
	for _, g := range ghosts {
		out.Ghosts = append(out.Ghosts, ghostToProto(g))
	}
	return connect.NewResponse(out), nil
}

func (s *Server) Annotate(ctx context.Context, req *connect.Request[pb.AnnotateRequest]) (*connect.Response[pb.AnnotateResponse], error) {
	actor := req.Msg.GetActor()
	if actor == "" {
		actor = "unknown"
	}
	ev, err := s.store.Annotate(ctx, req.Msg.GetRef(), req.Msg.GetStatus(), req.Msg.GetNote(), actor)
	if err != nil {
		return nil, storeErr(err)
	}
	s.log.Info("annotated", "drive", ev.Serial, "kind", ev.Kind, "actor", actor)
	return connect.NewResponse(&pb.AnnotateResponse{Event: eventToProto(ev)}), nil
}

func (s *Server) GetKernel(ctx context.Context, req *connect.Request[pb.GetKernelRequest]) (*connect.Response[pb.GetKernelResponse], error) {
	var since time.Time
	if t := req.Msg.GetSince(); t != nil {
		since = t.AsTime()
	}
	d, samples, err := s.store.KernelSamples(ctx, req.Msg.GetRef(), since)
	if err != nil {
		return nil, storeErr(err)
	}
	out := &pb.GetKernelResponse{Drive: driveToProto(d)}
	for _, k := range samples {
		out.Samples = append(out.Samples, kernelSampleToProto(k))
	}
	return connect.NewResponse(out), nil
}

func (s *Server) GetSmart(ctx context.Context, req *connect.Request[pb.GetSmartRequest]) (*connect.Response[pb.GetSmartResponse], error) {
	var since time.Time
	if t := req.Msg.GetSince(); t != nil {
		since = t.AsTime()
	}
	d, samples, raw, rawTS, err := s.store.SmartSamples(ctx, req.Msg.GetRef(), since, req.Msg.GetIncludeRaw())
	if err != nil {
		return nil, storeErr(err)
	}
	out := &pb.GetSmartResponse{Drive: driveToProto(d), RawJson: raw, RawTs: ts(rawTS)}
	for _, k := range samples {
		out.Samples = append(out.Samples, smartSampleToProto(k))
	}
	return connect.NewResponse(out), nil
}

func (s *Server) GetIO(ctx context.Context, req *connect.Request[pb.GetIORequest]) (*connect.Response[pb.GetIOResponse], error) {
	var since time.Time
	if t := req.Msg.GetSince(); t != nil {
		since = t.AsTime()
	}
	d, samples, err := s.store.IOSamples(ctx, req.Msg.GetRef(), since)
	if err != nil {
		return nil, storeErr(err)
	}
	out := &pb.GetIOResponse{Drive: driveToProto(d)}
	for _, k := range samples {
		out.Samples = append(out.Samples, ioSampleToProto(k))
	}
	return connect.NewResponse(out), nil
}

func (s *Server) CompareIO(ctx context.Context, req *connect.Request[pb.CompareIORequest]) (*connect.Response[pb.CompareIOResponse], error) {
	since := time.Now().Add(-24 * time.Hour)
	if t := req.Msg.GetSince(); t != nil {
		since = t.AsTime()
	}
	rows, err := s.store.CompareIO(ctx, req.Msg.GetHost(), since)
	if err != nil {
		return nil, storeErr(err)
	}
	out := &pb.CompareIOResponse{}
	for _, c := range rows {
		out.Rows = append(out.Rows, ioComparisonToProto(c))
	}
	return connect.NewResponse(out), nil
}

func (s *Server) MergeDrives(ctx context.Context, req *connect.Request[pb.MergeDrivesRequest]) (*connect.Response[pb.MergeDrivesResponse], error) {
	ev, err := s.store.MergeDrives(ctx, req.Msg.GetInto(), req.Msg.GetFrom(), actorOr(req.Msg.GetActor()))
	if err != nil {
		return nil, storeErr(err)
	}
	s.log.Info("drives merged", "into", ev.Serial, "detail", ev.Detail, "actor", req.Msg.GetActor())
	return connect.NewResponse(&pb.MergeDrivesResponse{Event: eventToProto(ev)}), nil
}

func (s *Server) MergeHosts(ctx context.Context, req *connect.Request[pb.MergeHostsRequest]) (*connect.Response[pb.MergeHostsResponse], error) {
	ev, err := s.store.MergeHosts(ctx, req.Msg.GetInto(), req.Msg.GetFrom(), actorOr(req.Msg.GetActor()))
	if err != nil {
		return nil, storeErr(err)
	}
	s.log.Info("hosts merged", "into", ev.Hostname, "detail", ev.Detail, "actor", req.Msg.GetActor())
	return connect.NewResponse(&pb.MergeHostsResponse{Event: eventToProto(ev)}), nil
}

func (s *Server) Rebuild(ctx context.Context, _ *connect.Request[pb.RebuildRequest]) (*connect.Response[pb.RebuildResponse], error) {
	res, err := s.store.Rebuild(ctx)
	if err != nil {
		s.log.Error("rebuild", "err", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.Warn("history rebuilt from snapshots", "snapshots", res.Snapshots, "placements", res.Placements, "events", res.Events)
	return connect.NewResponse(&pb.RebuildResponse{Snapshots: int32(res.Snapshots), Placements: int32(res.Placements), Events: int32(res.Events)}), nil
}

func actorOr(actor string) string {
	if actor == "" {
		return "unknown"
	}
	return actor
}

// storeErr maps store errors to connect codes: not found, invalid
// (ambiguous reference or bad argument), else internal.
func storeErr(err error) error {
	var amb *store.AmbiguousError
	switch {
	case errors.Is(err, store.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.As(err, &amb):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case strings.Contains(err.Error(), "is not one of") || strings.Contains(err.Error(), "nothing to record") || strings.Contains(err.Error(), "resolve to the same"):
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}
