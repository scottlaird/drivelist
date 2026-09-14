// Package demo writes a static copy of the fleet that the web interface
// can browse with no server behind it: one JSON file per call the page
// makes, the page itself marked as a demo, and the names in the data
// changed as a names file says. It is what the public demo is built from.
package demo

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Names says how the export renames things. Hosts maps real hostnames to
// the names shown; every other host is named by Others in turn, and the
// assignment is kept from one export to the next so a host keeps its
// number. Replace is applied to every string in every response, longest
// key first. Clear names proto fields (by their proto name, "machine_id")
// that are emptied wherever they appear.
type Names struct {
	Hosts   map[string]string `json:"hosts"`
	Others  string            `json:"others"`
	Replace map[string]string `json:"replace"`
	Clear   []string          `json:"clear"`
}

// ReadNames loads a names file.
func ReadNames(path string) (Names, error) {
	var n Names
	b, err := os.ReadFile(path)
	if err != nil {
		return n, err
	}
	if err := json.Unmarshal(b, &n); err != nil {
		return n, fmt.Errorf("%s: %w", path, err)
	}
	return n, nil
}

// Sanitizer rewrites the strings of proto messages.
type Sanitizer struct {
	mapping map[string]string // real hostname -> shown
	hostRE  *regexp.Regexp    // every real hostname as a whole word
	replace []pair
	clear   map[string]bool
}

type pair struct{ from, to string }

// NewSanitizer resolves names for hosts: the ones Names lists, then the
// ones prior already named (the mapping a previous export wrote), then
// new ones numbered after the highest number taken.
func NewSanitizer(names Names, hosts []string, prior map[string]string) *Sanitizer {
	s := &Sanitizer{mapping: map[string]string{}, clear: map[string]bool{}}
	for real, shown := range names.Hosts {
		s.mapping[real] = shown
	}
	others := names.Others
	if others == "" {
		others = "server%d"
	}
	taken := map[string]bool{}
	for _, shown := range s.mapping {
		taken[shown] = true
	}
	for real, shown := range prior {
		if _, ok := s.mapping[real]; !ok && shown != "" {
			s.mapping[real] = shown
			taken[shown] = true
		}
	}
	sorted := append([]string(nil), hosts...)
	sort.Strings(sorted)
	next := 1
	for _, real := range sorted {
		if real == "" {
			continue
		}
		if _, ok := s.mapping[real]; ok {
			continue
		}
		for taken[fmt.Sprintf(others, next)] {
			next++
		}
		s.mapping[real] = fmt.Sprintf(others, next)
		taken[s.mapping[real]] = true
	}
	// Longest first, so a host that is a prefix of another (or its FQDN)
	// cannot steal the match.
	reals := make([]string, 0, len(s.mapping))
	for real := range s.mapping {
		reals = append(reals, real)
	}
	sort.Slice(reals, func(i, j int) bool {
		return len(reals[i]) > len(reals[j]) || (len(reals[i]) == len(reals[j]) && reals[i] < reals[j])
	})
	if len(reals) > 0 {
		quoted := make([]string, len(reals))
		for i, r := range reals {
			quoted[i] = regexp.QuoteMeta(r)
		}
		s.hostRE = regexp.MustCompile(`\b(` + strings.Join(quoted, "|") + `)\b`)
	}
	for from, to := range names.Replace {
		if from != "" {
			s.replace = append(s.replace, pair{from, to})
		}
	}
	sort.Slice(s.replace, func(i, j int) bool {
		return len(s.replace[i].from) > len(s.replace[j].from) || (len(s.replace[i].from) == len(s.replace[j].from) && s.replace[i].from < s.replace[j].from)
	})
	for _, f := range names.Clear {
		s.clear[f] = true
	}
	return s
}

// Mapping is every real hostname and what it is shown as.
func (s *Sanitizer) Mapping() map[string]string {
	out := make(map[string]string, len(s.mapping))
	for k, v := range s.mapping {
		out[k] = v
	}
	return out
}

// Text rewrites one string: hostnames as whole words, then the literal
// replacements.
func (s *Sanitizer) Text(str string) string {
	if s.hostRE != nil {
		str = s.hostRE.ReplaceAllStringFunc(str, func(m string) string { return s.mapping[m] })
	}
	for _, p := range s.replace {
		str = strings.ReplaceAll(str, p.from, p.to)
	}
	return str
}

// Message rewrites every string in m, nested messages, lists and maps
// included, and empties the fields Names.Clear names.
func (s *Sanitizer) Message(m proto.Message) {
	s.message(m.ProtoReflect())
}

func (s *Sanitizer) message(m protoreflect.Message) {
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if !m.Has(fd) {
			continue
		}
		if s.clear[string(fd.Name())] {
			m.Clear(fd)
			continue
		}
		v := m.Get(fd)
		switch {
		case fd.IsList():
			l := m.Mutable(fd).List()
			for j := 0; j < l.Len(); j++ {
				switch fd.Kind() {
				case protoreflect.MessageKind:
					s.message(l.Get(j).Message())
				case protoreflect.StringKind:
					l.Set(j, protoreflect.ValueOfString(s.Text(l.Get(j).String())))
				}
			}
		case fd.IsMap():
			mp := m.Mutable(fd).Map()
			var keys []protoreflect.MapKey
			mp.Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool { keys = append(keys, k); return true })
			for _, k := range keys {
				switch fd.MapValue().Kind() {
				case protoreflect.MessageKind:
					s.message(mp.Get(k).Message())
				case protoreflect.StringKind:
					mp.Set(k, protoreflect.ValueOfString(s.Text(mp.Get(k).String())))
				}
			}
		case fd.Kind() == protoreflect.MessageKind:
			s.message(m.Mutable(fd).Message())
		case fd.Kind() == protoreflect.StringKind:
			m.Set(fd, protoreflect.ValueOfString(s.Text(v.String())))
		}
	}
}
