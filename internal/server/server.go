// Package server serves the drivelist.v1 API over connect: the Collector
// service agents report to and the Query service the CLI reads from, on one
// http.Handler, each behind its own bearer token.
package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
	"github.com/scottlaird/drivelist/internal/pb/drivelistv1/drivelistv1connect"
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
}

// Server implements both services.
type Server struct {
	store *store.Store
	cfg   Config
	log   *slog.Logger
}

// New returns a server over st. It fails rather than run without tokens.
func New(st *store.Store, cfg Config, log *slog.Logger) (*Server, error) {
	if cfg.AgentToken == "" || cfg.OperatorToken == "" {
		return nil, errors.New("both the agent token and the operator token are required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{store: st, cfg: cfg, log: log}, nil
}

// Handler mounts both services plus /healthz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(drivelistv1connect.NewCollectorHandler(s, connect.WithInterceptors(bearerAuth(s.cfg.AgentToken))))
	mux.Handle(drivelistv1connect.NewQueryHandler(s, connect.WithInterceptors(bearerAuth(s.cfg.OperatorToken))))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	return mux
}

// RunSweeper marks stale hosts once per interval until ctx ends.
func (s *Server) RunSweeper(ctx context.Context) {
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
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
	res, err := s.store.Ingest(ctx, r)
	if err != nil {
		s.log.Error("ingest", "host", r.Host.Hostname, "err", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !res.Accepted {
		s.log.Warn("report rejected", "host", r.Host.Hostname, "reason", res.RejectReason)
	} else if res.Changed {
		s.log.Info("inventory changed", "host", r.Host.Hostname, "devices", len(r.Devices))
	}
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

// storeErr maps store errors to connect codes: not found, invalid
// (ambiguous reference or bad argument), else internal.
func storeErr(err error) error {
	var amb *store.AmbiguousError
	switch {
	case errors.Is(err, store.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.As(err, &amb):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case strings.Contains(err.Error(), "is not one of") || strings.Contains(err.Error(), "nothing to record"):
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}
