package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
)

// spool is a directory of undeliverable reports, one protobuf file each,
// named by observed time so they replay in order. A run of reports with
// identical content keeps only its latest member, so a long outage with a
// quiet host costs one file, not one per tick; and the directory is capped,
// dropping the oldest, so a dead server cannot fill a disk.
type spool struct {
	dir      string
	max      int
	lastHash string // content hash of the newest entry, for collapsing
	lastName string
}

func openSpool(dir string, max int) (*spool, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &spool{dir: dir, max: max}, nil
}

func (s *spool) names() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pb") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// add writes req to the spool, collapsing it onto the previous entry when
// only the timestamp differs, and evicting the oldest past the cap.
func (s *spool) add(req *pb.ReportInventoryRequest) error {
	hash := contentHash(req)
	if s.lastName != "" && hash == s.lastHash {
		os.Remove(filepath.Join(s.dir, s.lastName))
	}
	data, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%020d.pb", req.GetObservedAt().AsTime().UnixNano())
	tmp := filepath.Join(s.dir, name+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, name)); err != nil {
		return err
	}
	s.lastHash, s.lastName = hash, name

	names, err := s.names()
	if err != nil {
		return err
	}
	for len(names) > s.max {
		os.Remove(filepath.Join(s.dir, names[0]))
		names = names[1:]
	}
	return nil
}

// oldest returns the oldest spooled report and a function that removes it,
// or nil when the spool is empty. An unreadable file is removed and
// skipped.
func (s *spool) oldest() (*pb.ReportInventoryRequest, func() error, error) {
	for {
		names, err := s.names()
		if err != nil {
			return nil, nil, err
		}
		if len(names) == 0 {
			return nil, nil, nil
		}
		path := filepath.Join(s.dir, names[0])
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		req := &pb.ReportInventoryRequest{}
		if err := proto.Unmarshal(data, req); err != nil {
			os.Remove(path)
			continue
		}
		return req, func() error {
			if names[0] == s.lastName {
				s.lastName, s.lastHash = "", ""
			}
			return os.Remove(path)
		}, nil
	}
}

func (s *spool) len() int {
	names, _ := s.names()
	return len(names)
}

// contentHash hashes a report with its timestamp cleared, so two reports of
// the same state compare equal.
func contentHash(req *pb.ReportInventoryRequest) string {
	clone := proto.Clone(req).(*pb.ReportInventoryRequest)
	clone.ObservedAt = nil
	data, _ := proto.MarshalOptions{Deterministic: true}.Marshal(clone)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
