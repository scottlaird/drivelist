package demo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
	"github.com/scottlaird/drivelist/internal/pb/drivelistv1/drivelistv1connect"
	"github.com/scottlaird/drivelist/internal/server"
)

// The windows the page asks for, in days; see sinceDays() in ui/app.js.
const (
	compareWindow = 1
	ioWindow      = 7
	kernelWindow  = 7
	smartWindow   = 30
	sasWindow     = 7
)

// The event kinds the page's filter offers; see pageEvents in ui/app.js.
var eventKinds = []string{"first_seen", "appeared", "vanished", "reappeared", "moved_host", "moved_bay", "use_changed", "enclosure_renamed", "member_state_changed", "status_changed", "note", "merged", "host_merged", "smart_warning", "kernel_warning", "sas_link_changed", "sas_attached_changed", "sas_port_changed", "sas_errors", "sas_node_changed", "host_first_seen", "host_stale", "host_resumed", "report_degraded", "pool_missing_member", "identity_conflict"}

// Manifest is demo.json: what the page reads to know it is a demo.
type Manifest struct {
	AsOf   time.Time `json:"asOf"`   // when the export ran; the page's clock stops here
	Hosts  int       `json:"hosts"`  // for the banner
	Drives int       `json:"drives"` // present drives
	Files  int       `json:"files"`  // data files written
}

// Export writes the site into dir: the page, demo.json, and data/PROC/KEY.json
// for every call the page can make against this fleet, each response
// sanitized. The mapping of real to shown hostnames is read from and
// written back to dir/mapping.json so numbering is stable across
// exports. log, if not nil, gets a line per stage.
func Export(ctx context.Context, q drivelistv1connect.QueryClient, dir string, names Names, log func(string, ...any)) (*Manifest, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	now := time.Now()
	since := func(days int) *timestamppb.Timestamp {
		return timestamppb.New(now.Add(-time.Duration(days) * 24 * time.Hour))
	}

	hosts, err := q.ListHosts(ctx, connect.NewRequest(&pb.ListHostsRequest{}))
	if err != nil {
		return nil, err
	}
	var hostnames []string
	for _, h := range hosts.Msg.Hosts {
		hostnames = append(hostnames, h.Hostname)
	}
	prior := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(dir, "mapping.json")); err == nil {
		_ = json.Unmarshal(b, &prior)
	}
	san := NewSanitizer(names, hostnames, prior)

	// The data directory is rebuilt from nothing, so a drive that left the
	// fleet leaves the demo too.
	data := filepath.Join(dir, "data")
	if err := os.RemoveAll(data); err != nil {
		return nil, err
	}
	w := &writer{dir: data, san: san}

	// Fleet-wide.
	w.save("ListHosts", nil, hosts.Msg)
	drives, err := q.ListDrives(ctx, connect.NewRequest(&pb.ListDrivesRequest{}))
	if err != nil {
		return nil, err
	}
	w.save("ListDrives", nil, drives.Msg)
	encls, err := q.ListEnclosures(ctx, connect.NewRequest(&pb.ListEnclosuresRequest{}))
	if err != nil {
		return nil, err
	}
	w.save("ListEnclosures", nil, encls.Msg)
	w.call("ListMissing", nil, func() (proto.Message, error) {
		return unwrap(q.ListMissing(ctx, connect.NewRequest(&pb.ListMissingRequest{})))
	})
	w.call("ListSmart", nil, func() (proto.Message, error) {
		return unwrap(q.ListSmart(ctx, connect.NewRequest(&pb.ListSmartRequest{})))
	})
	w.call("ListSmart", map[string]any{"problems": true}, func() (proto.Message, error) {
		return unwrap(q.ListSmart(ctx, connect.NewRequest(&pb.ListSmartRequest{Problems: true})))
	})
	w.call("ListSASErrors", map[string]any{"since": true}, func() (proto.Message, error) {
		return unwrap(q.ListSASErrors(ctx, connect.NewRequest(&pb.ListSASErrorsRequest{Since: since(sasWindow)})))
	})
	w.call("CompareIO", map[string]any{"since": true}, func() (proto.Message, error) {
		return unwrap(q.CompareIO(ctx, connect.NewRequest(&pb.CompareIORequest{Since: since(compareWindow)})))
	})
	for _, limit := range []int{15, 500} {
		w.call("ListEvents", map[string]any{"limit": limit}, func() (proto.Message, error) {
			return unwrap(q.ListEvents(ctx, connect.NewRequest(&pb.ListEventsRequest{Limit: int32(limit)})))
		})
	}
	for _, kind := range eventKinds {
		w.call("ListEvents", map[string]any{"limit": 500, "kinds": []string{kind}}, func() (proto.Message, error) {
			return unwrap(q.ListEvents(ctx, connect.NewRequest(&pb.ListEventsRequest{Limit: 500, Kinds: []string{kind}})))
		})
	}
	log("fleet-wide listings written", "files", w.count())

	// Per host. The page asks by the shown name, so the request goes by
	// the real one and the file by the shown one.
	for _, h := range hosts.Msg.Hosts {
		real, shown := h.Hostname, san.Text(h.Hostname)
		w.call("ListDrives", map[string]any{"host": shown}, func() (proto.Message, error) {
			return unwrap(q.ListDrives(ctx, connect.NewRequest(&pb.ListDrivesRequest{Host: real})))
		})
		w.call("ListEvents", map[string]any{"host": shown, "limit": 100}, func() (proto.Message, error) {
			return unwrap(q.ListEvents(ctx, connect.NewRequest(&pb.ListEventsRequest{Host: real, Limit: 100})))
		})
		w.call("CompareIO", map[string]any{"host": shown}, func() (proto.Message, error) {
			return unwrap(q.CompareIO(ctx, connect.NewRequest(&pb.CompareIORequest{Host: real, Since: since(compareWindow)})))
		})
		w.call("GetSAS", map[string]any{"host": shown}, func() (proto.Message, error) {
			return unwrap(q.GetSAS(ctx, connect.NewRequest(&pb.GetSASRequest{Host: real})))
		})
	}
	log("hosts written", "hosts", len(hosts.Msg.Hosts), "files", w.count())

	// Per enclosure.
	for _, e := range encls.Msg.Enclosures {
		key := e.Enclosure
		w.call("ListBays", map[string]any{"ref": san.Text(key)}, func() (proto.Message, error) {
			return unwrap(q.ListBays(ctx, connect.NewRequest(&pb.ListBaysRequest{Ref: key})))
		})
	}

	// Per drive, several at a time; the page links drives by serial.
	serials := map[string]bool{}
	for _, d := range drives.Msg.Drives {
		if d.Serial != "" {
			serials[d.Serial] = true
		}
	}
	list := make([]string, 0, len(serials))
	for s := range serials {
		list = append(list, s)
	}
	sort.Strings(list)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, serial := range list {
		wg.Add(1)
		sem <- struct{}{}
		go func(serial string) {
			defer wg.Done()
			defer func() { <-sem }()
			ref := map[string]any{"ref": san.Text(serial)}
			w.call("GetDrive", ref, func() (proto.Message, error) {
				return unwrap(q.GetDrive(ctx, connect.NewRequest(&pb.GetDriveRequest{Ref: serial})))
			})
			w.call("GetDriveHistory", ref, func() (proto.Message, error) {
				return unwrap(q.GetDriveHistory(ctx, connect.NewRequest(&pb.GetDriveHistoryRequest{Ref: serial})))
			})
			w.call("GetIO", ref, func() (proto.Message, error) {
				return unwrap(q.GetIO(ctx, connect.NewRequest(&pb.GetIORequest{Ref: serial, Since: since(ioWindow)})))
			})
			w.call("GetKernel", ref, func() (proto.Message, error) {
				return unwrap(q.GetKernel(ctx, connect.NewRequest(&pb.GetKernelRequest{Ref: serial, Since: since(kernelWindow)})))
			})
			w.call("GetSmart", ref, func() (proto.Message, error) {
				return unwrap(q.GetSmart(ctx, connect.NewRequest(&pb.GetSmartRequest{Ref: serial, Since: since(smartWindow)})))
			})
		}(serial)
	}
	wg.Wait()
	log("drives written", "drives", len(list), "files", w.count())
	if err := w.err(); err != nil {
		return nil, err
	}

	// The page, marked as a demo, and what it needs to know.
	present := 0
	for _, d := range drives.Msg.Drives {
		if d.Current != nil {
			present++
		}
	}
	man := &Manifest{AsOf: now, Hosts: len(hosts.Msg.Hosts), Drives: present, Files: w.count()}
	if err := writeSite(dir, man, san.Mapping()); err != nil {
		return nil, err
	}
	return man, nil
}

// writer saves sanitized responses under dir, remembering the first
// failure; a failed call is logged and left out rather than stopping
// the export, since a demo with one drive page missing is still a demo.
type writer struct {
	dir string
	san *Sanitizer

	mu    sync.Mutex
	n     int
	first error
}

func (w *writer) call(proc string, body map[string]any, do func() (proto.Message, error)) {
	msg, err := do()
	if err != nil {
		w.mu.Lock()
		if w.first == nil {
			w.first = fmt.Errorf("%s %s: %w", proc, Key(body), err)
		}
		w.mu.Unlock()
		return
	}
	w.save(proc, body, msg)
}

func (w *writer) save(proc string, body map[string]any, msg proto.Message) {
	// A copy: the caller still needs the real names to ask for more.
	msg = proto.Clone(msg)
	w.san.Message(msg)
	b, err := protojson.MarshalOptions{Multiline: true, Indent: " "}.Marshal(msg)
	if err == nil {
		dir := filepath.Join(w.dir, proc)
		err = os.MkdirAll(dir, 0o755)
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, Key(body)+".json"), append(b, '\n'), 0o644)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		if w.first == nil {
			w.first = fmt.Errorf("%s %s: %w", proc, Key(body), err)
		}
		return
	}
	w.n++
}

func (w *writer) count() int { w.mu.Lock(); defer w.mu.Unlock(); return w.n }
func (w *writer) err() error { w.mu.Lock(); defer w.mu.Unlock(); return w.first }

func unwrap[T any](res *connect.Response[T], err error) (proto.Message, error) {
	if err != nil {
		return nil, err
	}
	m, ok := any(res.Msg).(proto.Message)
	if !ok {
		return nil, errors.New("not a proto message")
	}
	return m, nil
}

// writeSite copies the page in, marked as a demo: a meta tag names the
// manifest, which app.js looks for, and the policy the server would
// send as a header rides in a meta tag since there is no server.
func writeSite(dir string, man *Manifest, mapping map[string]string) error {
	index, err := server.UIFile("index.html")
	if err != nil {
		return err
	}
	const anchor = `<link rel="stylesheet" href="style.css">`
	if !strings.Contains(string(index), anchor) {
		return errors.New("index.html has no stylesheet link to hang the demo marker on")
	}
	marked := strings.Replace(string(index), anchor,
		`<meta http-equiv="Content-Security-Policy" content="`+server.UIPolicy+`">`+"\n"+
			`<meta name="drivelist-demo" content="demo.json">`+"\n"+anchor, 1)
	files := map[string][]byte{"index.html": []byte(marked), ".nojekyll": nil}
	for _, name := range []string{"app.js", "style.css"} {
		b, err := server.UIFile(name)
		if err != nil {
			return err
		}
		files[name] = b
	}
	if files["demo.json"], err = json.MarshalIndent(man, "", " "); err != nil {
		return err
	}
	if files["mapping.json"], err = json.MarshalIndent(mapping, "", " "); err != nil {
		return err
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}
