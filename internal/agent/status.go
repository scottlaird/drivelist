package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
)

// StatusCache is what the agent writes after every accepted report: the
// server's status for each drive on this host, so the plain listing can
// show it without network access.
type StatusCache struct {
	Updated time.Time     `json:"updated"`
	Drives  []DriveStatus `json:"drives"`
}

// DriveStatus is one drive's server-side status.
type DriveStatus struct {
	WWN    string `json:"wwn,omitempty"`
	Model  string `json:"model,omitempty"`
	Serial string `json:"serial,omitempty"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// WriteStatusCache writes the cache atomically.
func WriteStatusCache(path string, now time.Time, statuses []*pb.DriveStatus) error {
	c := StatusCache{Updated: now.UTC(), Drives: []DriveStatus{}}
	for _, s := range statuses {
		id := s.GetIdentity()
		c.Drives = append(c.Drives, DriveStatus{WWN: id.GetWwn(), Model: id.GetModel(), Serial: id.GetSerial(), Status: s.GetStatus(), Note: s.GetNote()})
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadStatusCache loads the cache; a missing file is an empty cache, not
// an error.
func ReadStatusCache(path string) (*StatusCache, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &StatusCache{}, nil
	}
	if err != nil {
		return nil, err
	}
	c := &StatusCache{}
	return c, json.Unmarshal(data, c)
}

// Lookup returns the status for a drive by WWN, or by model and serial,
// and "" when the cache has nothing for it.
func (c *StatusCache) Lookup(wwn, model, serial string) (status, note string) {
	if c == nil {
		return "", ""
	}
	for _, d := range c.Drives {
		if (wwn != "" && d.WWN == wwn) || (serial != "" && d.Serial == serial && d.Model == model) {
			return d.Status, d.Note
		}
	}
	return "", ""
}
