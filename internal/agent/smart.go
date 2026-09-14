package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/scottlaird/drivelist/collect"
	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
	"github.com/scottlaird/drivelist/internal/report"
)

// SmartConfig turns on SMART sampling.
type SmartConfig struct {
	Interval time.Duration       // between full passes; the server may change it. 0: 6h
	Run      collect.SmartRunner // nil: smartctl from PATH
	// Settle is how long after a request (a new drive, a kernel warning)
	// to wait before sampling just those drives. 0: 10s
	Settle time.Duration
}

// smartState is the agent's SMART bookkeeping.
type smartState struct {
	cfg SmartConfig

	mu       sync.Mutex
	devTypes map[string]string    // device -> smartctl -d type that worked
	lastRaw  map[string]time.Time // device -> when raw JSON was last sent
	lastHash map[string]string    // device -> hash of the last summary sent (temperature and hours aside)
	pending  map[string]bool      // devices requested for an out-of-band sample
	timer    *time.Timer          // fires the out-of-band sample
}

// rawEvery is how often the raw smartctl document is sent when nothing
// else changed.
const rawEvery = 24 * time.Hour

// EnableSmart turns on SMART sampling: a full pass right after the first
// report (a baseline for every drive), then every interval; and a sample
// of any drive that newly appears or that the kernel warns about.
func (a *Agent) EnableSmart(cfg SmartConfig) {
	if cfg.Interval <= 0 {
		cfg.Interval = 6 * time.Hour
	}
	if cfg.Run == nil {
		cfg.Run = collect.DefaultSmartRunner
	}
	if cfg.Settle <= 0 {
		cfg.Settle = 10 * time.Second
	}
	a.smart = &smartState{cfg: cfg, devTypes: map[string]string{}, lastRaw: map[string]time.Time{}, lastHash: map[string]string{}, pending: map[string]bool{}}
}

// RequestSmart asks for the named devices to be sampled soon, outside the
// regular pass. Requests within the settle window share one pass.
func (a *Agent) RequestSmart(devNames ...string) {
	st := a.smart
	if st == nil || len(devNames) == 0 {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, n := range devNames {
		st.pending[n] = true
	}
	if st.timer == nil {
		st.timer = time.AfterFunc(st.cfg.Settle, func() {
			select {
			case a.smartReq <- struct{}{}:
			default:
			}
		})
	}
}

// takePending returns and clears the requested devices.
func (st *smartState) takePending() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	names := make([]string, 0, len(st.pending))
	for n := range st.pending {
		names = append(names, n)
	}
	st.pending = map[string]bool{}
	st.timer = nil
	sort.Strings(names)
	return names
}

// smartPass samples the named devices (all known ones when names is nil)
// one at a time and sends the results. A drive that hangs smartctl costs
// 30 s and a "timeout" sample, not the whole pass.
func (a *Agent) smartPass(ctx context.Context, reason string, names []string) {
	st := a.smart
	if st == nil {
		return
	}
	if names == nil {
		names = a.ids.names()
	}
	req := &pb.ReportSmartRequest{Host: a.cfg.Host}
	skipped := map[string]int{}
	for _, name := range names {
		id, ok := a.ids.lookup(name, "")
		if !ok {
			continue
		}
		st.mu.Lock()
		devType := st.devTypes[name]
		st.mu.Unlock()
		sample, usedType := collect.SampleSmart(ctx, st.cfg.Run, name, devType, a.now())
		if usedType != "" {
			st.mu.Lock()
			st.devTypes[name] = usedType
			st.mu.Unlock()
		}
		if ctx.Err() != nil {
			return
		}
		if sample.Skipped != "" {
			skipped[sample.Skipped]++
		}
		req.Samples = append(req.Samples, a.smartToProto(name, id, sample))
	}
	if len(req.Samples) == 0 {
		return
	}
	if _, err := a.send.Smart(ctx, req); err != nil {
		a.log.Warn("smart samples not sent; the next pass will resample", "err", err, "samples", len(req.Samples))
		return
	}
	a.log.Info("smart pass sent", "reason", reason, "samples", len(req.Samples), "skipped", skipped)
}

// smartToProto converts a sample, attaching the raw document when it is
// due (daily) or the summary changed in something other than temperature
// or hours.
func (a *Agent) smartToProto(name string, id *pb.DriveIdentity, s collect.SmartSample) *pb.SmartSample {
	out := &pb.SmartSample{Identity: id, DevName: name, Ts: timestamppb.New(s.At), Skipped: s.Skipped}
	if s.Summary == nil {
		return out
	}
	out.Summary = report.SmartSummary(s.Summary)
	st := a.smart
	st.mu.Lock()
	defer st.mu.Unlock()
	hash := summaryHash(s.Summary)
	if s.Raw != nil && (s.At.Sub(st.lastRaw[name]) >= rawEvery || hash != st.lastHash[name]) {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		w.Write(s.Raw)
		w.Close()
		out.RawJsonGz = buf.Bytes()
		st.lastRaw[name] = s.At
	}
	st.lastHash[name] = hash
	return out
}

// summaryHash identifies a summary by its values, temperature and hours
// aside, since those change every sample without meaning anything.
func summaryHash(s *collect.SmartSummary) string {
	v := func(p *uint64) string {
		if p == nil {
			return "-"
		}
		return fmt.Sprint(*p)
	}
	parts := []string{s.Protocol, s.SelftestLast, v(s.Reallocated), v(s.Pending), v(s.Uncorrectable), v(s.CRCErrors), v(s.ReadBytes), v(s.WriteBytes)}
	if s.Healthy != nil {
		parts = append(parts, fmt.Sprint(*s.Healthy))
	}
	if s.PercentUsed != nil {
		parts = append(parts, fmt.Sprint(*s.PercentUsed))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:8])
}
