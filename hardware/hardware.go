// Package hardware carries what drivelist knows about specific enclosure
// models: what a person calls each bay, which firmware identities (an SES
// bay number, a PCIe slot name, an ATA port) land in it, and which bays
// exist at all, whether or not anything has ever been seen in them.
//
// Profiles are JSON files under profiles/, embedded in the binary, one per
// model. The server applies them as a view: placements keep the firmware
// bay, and the profile says how to show it. A profile matches an
// enclosure by the model string the agent reports (the expander's vendor
// and product for an SES enclosure, the DMI vendor and product name for a
// chassis), optionally narrowed by the board name.
package hardware

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed profiles/*.json
var embedded embed.FS

// Profile describes one enclosure model.
type Profile struct {
	Match  Match   `json:"match"`
	Title  string  `json:"title"`            // what to call the model: "ASUS RS500A-E10-RS12U, 12 x 2.5\" front bays"
	Layout *Layout `json:"layout,omitempty"` // how the bays are arranged, for the grid view
	Bays   []Bay   `json:"bays"`
	Notes  string  `json:"notes,omitempty"`
	File   string  `json:"-"` // where it was loaded from
}

// Match selects the enclosures a profile applies to. Model is compared
// exactly after trimming; Board, when set, must equal the DMI board name
// as the agent reports it in the model string's absence.
type Match struct {
	Model string `json:"model"`
	Board string `json:"board,omitempty"`
}

// Layout is the physical arrangement: bays fill columns top to bottom
// then left to right ("column-major", the default) or rows left to right
// then top to bottom ("row-major"), in the order they are listed.
type Layout struct {
	Rows    int    `json:"rows"`
	Columns int    `json:"columns"`
	Order   string `json:"order,omitempty"`
}

// Bay is one physical bay and the firmware identities that land in it. A
// bay wired for more than one kind of drive lists each: a U.2 drive in it
// arrives as NVMe in the PCIe slot, a SATA drive on the ATA port.
type Bay struct {
	Bay  string `json:"bay"`           // what the manual calls it: "1", "front 3", "A0"
	SAS  string `json:"sas,omitempty"` // SES bay_identifier
	PCI  string `json:"pci,omitempty"` // hotplug slot name or SMBIOS designation
	ATA  string `json:"ata,omitempty"` // "ata3"
	Note string `json:"note,omitempty"`
}

// Kinds are the firmware identity kinds a bay can carry, as the fields
// above and as Kind returns them.
var Kinds = []string{"sas", "pci", "ata"}

// Kind says which identity kind an enclosure's bays carry, from the node
// the agent says reaches the enclosure: an expander or HBA means SES bay
// numbers, "pci" means slot names, "ata" means ports.
func Kind(via string) string {
	switch {
	case via == "pci", via == "ata":
		return via
	case strings.HasPrefix(via, "expander-"), strings.HasPrefix(via, "host"):
		return "sas"
	}
	return ""
}

// ID returns the identity a bay carries for a kind.
func (b Bay) ID(kind string) string {
	switch kind {
	case "sas":
		return b.SAS
	case "pci":
		return b.PCI
	case "ata":
		return b.ATA
	}
	return ""
}

// Label returns the bay a firmware identity lands in, if the profile
// maps it.
func (p *Profile) Label(kind, id string) (string, bool) {
	if p == nil || id == "" {
		return "", false
	}
	for _, b := range p.Bays {
		if b.ID(kind) == id {
			return b.Bay, true
		}
	}
	return "", false
}

// Set is the loaded profiles.
type Set struct {
	profiles []*Profile
}

// Embedded returns the profiles built into the binary.
func Embedded() *Set {
	s, err := loadFS(embedded, "profiles")
	if err != nil {
		panic("hardware: embedded profiles: " + err.Error()) // the test catches this before a release
	}
	return s
}

// Load returns the embedded profiles plus those in dir, which override
// embedded ones matching the same model: the way to try a profile before
// contributing it. An empty dir means embedded only.
func Load(dir string) (*Set, error) {
	s := Embedded()
	if dir == "" {
		return s, nil
	}
	extra, err := loadFS(os.DirFS(dir), ".")
	if err != nil {
		return nil, fmt.Errorf("hardware profiles in %s: %w", dir, err)
	}
	for _, p := range extra.profiles {
		p.File = filepath.Join(dir, p.File)
		replaced := false
		for i, q := range s.profiles {
			if q.Match == p.Match {
				s.profiles[i], replaced = p, true
			}
		}
		if !replaced {
			s.profiles = append(s.profiles, p)
		}
	}
	return s, nil
}

func loadFS(fsys fs.FS, root string) (*Set, error) {
	names, err := fs.Glob(fsys, path(root, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	s := &Set{}
	seen := map[Match]string{}
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		var p Profile
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		p.File = name
		if err := p.validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if other, dup := seen[p.Match]; dup {
			return nil, fmt.Errorf("%s: matches the same model as %s", name, other)
		}
		seen[p.Match] = name
		s.profiles = append(s.profiles, &p)
	}
	return s, nil
}

func path(root, pattern string) string {
	if root == "." {
		return pattern
	}
	return root + "/" + pattern
}

func (p *Profile) validate() error {
	p.Match.Model = strings.TrimSpace(p.Match.Model)
	if p.Match.Model == "" {
		return fmt.Errorf("match.model is required")
	}
	if p.Title == "" {
		return fmt.Errorf("title is required")
	}
	if len(p.Bays) == 0 {
		return fmt.Errorf("at least one bay is required")
	}
	if l := p.Layout; l != nil {
		if l.Rows <= 0 || l.Columns <= 0 || l.Rows*l.Columns < len(p.Bays) {
			return fmt.Errorf("layout %dx%d does not hold %d bays", l.Rows, l.Columns, len(p.Bays))
		}
		switch l.Order {
		case "", "column-major", "row-major":
		default:
			return fmt.Errorf("layout order %q: want column-major or row-major", l.Order)
		}
	}
	bays := map[string]bool{}
	ids := map[string]string{}
	for _, b := range p.Bays {
		if b.Bay == "" {
			return fmt.Errorf("a bay has no name")
		}
		if bays[b.Bay] {
			return fmt.Errorf("bay %q listed twice", b.Bay)
		}
		bays[b.Bay] = true
		for _, k := range Kinds {
			id := b.ID(k)
			if id == "" {
				continue
			}
			if other, dup := ids[k+":"+id]; dup {
				return fmt.Errorf("%s %s lands in both bay %q and bay %q", k, id, other, b.Bay)
			}
			ids[k+":"+id] = b.Bay
		}
	}
	return nil
}

// Lookup returns the profile for an enclosure model, or nil. A profile
// with a board narrows to that board; without one it matches any.
func (s *Set) Lookup(model, board string) *Profile {
	model = strings.TrimSpace(model)
	var loose *Profile
	for _, p := range s.profiles {
		if p.Match.Model != model {
			continue
		}
		if p.Match.Board == "" {
			loose = p
			continue
		}
		if p.Match.Board == board {
			return p
		}
	}
	return loose
}

// All returns every profile, in file order.
func (s *Set) All() []*Profile {
	return append([]*Profile(nil), s.profiles...)
}

// Position returns a bay's row and column in the layout (0-based), by its
// index in the list.
func (l *Layout) Position(i int) (row, col int) {
	if l == nil {
		return 0, i
	}
	if l.Order == "row-major" {
		return i / l.Columns, i % l.Columns
	}
	return i % l.Rows, i / l.Rows
}
