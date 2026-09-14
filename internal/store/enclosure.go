package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/scottlaird/drivelist/hardware"
)

// Enclosure is one physical place drives sit in on one host: a shelf, a
// backplane, a server's front panel.
type Enclosure struct {
	Key       string // what placements carry: the SES enclosure id, else the reaching node's SAS address, else its kernel name
	Hostname  string
	Via       string // the kernel's current name for the node that reaches it: "expander-11:0", "host11"
	Product   string // vendor and product of that node, from the SAS topology; "" if unknown
	Board     string // the DMI board name, when the enclosure is a chassis
	Name      string // what a person called it
	Note      string
	Drives    int    // drives currently placed in it
	Bays      int    // bays seen in it, empty ones included, or declared by the profile if more
	Profile   string // title of the hardware profile that matched, "" if none
	FirstSeen time.Time
	LastSeen  time.Time
}

// ListEnclosures returns every enclosure a host has reported or has a
// drive placed in, by host. The two views join on the key; the product
// comes from the SAS node that reaches the enclosure.
func (s *Store) ListEnclosures(ctx context.Context) ([]Enclosure, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH placed AS (
			SELECT p.host_id, p.enclosure, MAX(p.enclosure_via) AS via, COUNT(*) AS drives, MIN(p.first_seen) AS first_seen, MAX(p.last_seen) AS last_seen
			FROM placement p WHERE p.ended_at IS NULL AND p.enclosure != '' GROUP BY p.host_id, p.enclosure
		), known AS (
			SELECT host_id, enclosure FROM enclosure UNION SELECT host_id, enclosure FROM placed
		)
		SELECT k.enclosure, h.hostname, COALESCE(e.via, pl.via, ''), COALESCE(e.via_address, ''), COALESCE(e.product, ''), COALESCE(e.board, ''), COALESCE(x.name, ''), COALESCE(x.note, ''),
		       COALESCE(pl.drives, 0), COALESCE(e.bays, 0), COALESCE(MIN(e.first_seen, pl.first_seen), e.first_seen, pl.first_seen), COALESCE(MAX(e.last_seen, pl.last_seen), e.last_seen, pl.last_seen)
		FROM known k JOIN host h USING (host_id)
		LEFT JOIN enclosure e ON e.host_id = k.host_id AND e.enclosure = k.enclosure
		LEFT JOIN placed pl ON pl.host_id = k.host_id AND pl.enclosure = k.enclosure
		LEFT JOIN enclosure_name x ON x.enclosure = k.enclosure
		ORDER BY h.hostname, COALESCE(e.via, pl.via, ''), k.enclosure`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Enclosure
	var viaAddrs []string
	for rows.Next() {
		var e Enclosure
		var viaAddr string
		var first, last int64
		if err := rows.Scan(&e.Key, &e.Hostname, &e.Via, &viaAddr, &e.Product, &e.Board, &e.Name, &e.Note, &e.Drives, &e.Bays, &first, &last); err != nil {
			return nil, err
		}
		e.FirstSeen, e.LastSeen = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		out = append(out, e)
		viaAddrs = append(viaAddrs, viaAddr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The product of the node that reaches each enclosure. Before a 0.7
	// agent reports, the key itself is the expander's address.
	products, err := s.sasNodeProducts(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Product == "" {
			if p, ok := products[hostKey{out[i].Hostname, viaAddrs[i]}]; ok {
				out[i].Product = p
			} else if p, ok := products[hostKey{out[i].Hostname, out[i].Key}]; ok {
				out[i].Product = p
			}
		}
		if p := s.profiles().Lookup(out[i].Product, out[i].Board); p != nil {
			out[i].Profile = p.Title
			out[i].Bays = max(out[i].Bays, len(p.Bays))
		}
	}
	return out, nil
}

// BayView is one bay of an enclosure, as ListBays shows it.
type BayView struct {
	Label    string   // the profile's name, else the firmware identity
	IDs      []string // "sas:54", "pci:9-1", "ata:ata3"
	Declared bool     // the profile lists it
	Present  bool     // a drive is in it
	DevName  string
	Serial   string
	Model    string
	Status   string
	Uses     []string
}

// ListBays shows an enclosure bay by bay: the profile's bays in its order
// with what sits in each, then any occupied bay the profile does not
// know, which is the list a profile author needs.
func (s *Store) ListBays(ctx context.Context, ref string) (Enclosure, []BayView, *hardware.Layout, error) {
	key, err := s.ResolveEnclosure(ctx, ref)
	if err != nil {
		return Enclosure{}, nil, nil, err
	}
	all, err := s.ListEnclosures(ctx)
	if err != nil {
		return Enclosure{}, nil, nil, err
	}
	var enc Enclosure
	for _, e := range all {
		if e.Key == key {
			enc = e
		}
	}
	if enc.Key == "" {
		enc = Enclosure{Key: key}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.bay, p.enclosure_via, p.dev_name, d.serial, d.model, d.status, p.uses
		FROM placement p JOIN drive d USING (drive_id) WHERE p.enclosure = ? AND p.ended_at IS NULL ORDER BY p.bay`, key)
	if err != nil {
		return Enclosure{}, nil, nil, err
	}
	defer rows.Close()
	type occupant struct {
		bay, kind string
		view      BayView
	}
	var occupants []occupant
	for rows.Next() {
		var o occupant
		var uses string
		if err := rows.Scan(&o.bay, &o.kind, &o.view.DevName, &o.view.Serial, &o.view.Model, &o.view.Status, &uses); err != nil {
			return Enclosure{}, nil, nil, err
		}
		o.kind = hardware.Kind(o.kind)
		o.view.Present = true
		json.Unmarshal([]byte(uses), &o.view.Uses)
		occupants = append(occupants, o)
	}
	if err := rows.Err(); err != nil {
		return Enclosure{}, nil, nil, err
	}
	profile := s.profiles().Lookup(enc.Product, enc.Board)
	var out []BayView
	used := make([]bool, len(occupants))
	if profile != nil {
		for _, b := range profile.Bays {
			v := BayView{Label: b.Bay, Declared: true}
			for _, k := range hardware.Kinds {
				if id := b.ID(k); id != "" {
					v.IDs = append(v.IDs, k+":"+id)
				}
			}
			for i, o := range occupants {
				if !used[i] && b.ID(o.kind) == o.bay {
					used[i] = true
					view := o.view
					view.Label, view.IDs, view.Declared = v.Label, v.IDs, true
					v = view
				}
			}
			out = append(out, v)
		}
	}
	for i, o := range occupants {
		if used[i] {
			continue
		}
		v := o.view
		v.Label = o.bay
		if o.kind != "" {
			v.IDs = []string{o.kind + ":" + o.bay}
		}
		out = append(out, v)
	}
	var layout *hardware.Layout
	if profile != nil {
		layout = profile.Layout
	}
	return enc, out, layout, nil
}

// EnclosureModel is what an enclosure is, for profile lookup.
type EnclosureModel struct{ Model, Board string }

// EnclosureModels returns each known enclosure key's model, for
// decorating events with bay labels.
func (s *Store) EnclosureModels(ctx context.Context) (map[string]EnclosureModel, error) {
	all, err := s.ListEnclosures(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]EnclosureModel{}
	for _, e := range all {
		out[e.Key] = EnclosureModel{e.Product, e.Board}
	}
	return out, nil
}

// BayLabel is the profile's name for a firmware bay in an enclosure of
// the given model, or "".
func (s *Store) BayLabel(m EnclosureModel, via, bay string) string {
	label, _ := s.profiles().Lookup(m.Model, m.Board).Label(hardware.Kind(via), bay)
	return label
}

type hostKey struct{ host, key string }

func (s *Store) sasNodeProducts(ctx context.Context) (map[hostKey]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT h.hostname, n.address, TRIM(n.vendor || ' ' || n.product) FROM sas_node n JOIN host h USING (host_id) WHERE n.gone_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[hostKey]string{}
	for rows.Next() {
		var k hostKey
		var p string
		if err := rows.Scan(&k.host, &k.key, &p); err != nil {
			return nil, err
		}
		out[k] = p
	}
	return out, rows.Err()
}

// ResolveEnclosure finds an enclosure by its key, by an unambiguous part
// of the key (case-insensitively, with or without 0x), or by the name of
// the node that reaches it when only one host has one so named.
func (s *Store) ResolveEnclosure(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("enclosure reference is empty: %w", ErrNotFound)
	}
	all, err := s.ListEnclosures(ctx)
	if err != nil {
		return "", err
	}
	lower := strings.ToLower(ref)
	var exact, partial, byVia []Enclosure
	for _, e := range all {
		k := strings.ToLower(e.Key)
		switch {
		case k == lower || k == "0x"+lower:
			exact = append(exact, e)
		case strings.Contains(k, lower):
			partial = append(partial, e)
		case e.Via == ref || e.Name == ref:
			byVia = append(byVia, e)
		}
	}
	pick := exact
	if len(pick) == 0 {
		pick = partial
	}
	if len(pick) == 0 {
		pick = byVia
	}
	keys := map[string]bool{}
	for _, e := range pick {
		keys[e.Key] = true
	}
	switch len(keys) {
	case 0:
		// A name given earlier still resolves once nothing reports it.
		var key string
		err := s.db.QueryRowContext(ctx, `SELECT enclosure FROM enclosure_name WHERE enclosure = ? OR lower(enclosure) = ? OR lower(enclosure) = '0x' || ? OR name = ?`, ref, lower, lower, ref).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("enclosure %q: %w", ref, ErrNotFound)
		}
		return key, err
	case 1:
		return pick[0].Key, nil
	}
	names := make([]string, 0, len(pick))
	for _, e := range pick {
		names = append(names, fmt.Sprintf("%s (%s on %s)", e.Key, e.Via, e.Hostname))
	}
	sort.Strings(names)
	return "", &AmbiguousError{Ref: ref, Candidates: names}
}

// NameEnclosure records what a person calls an enclosure, or clears it
// with an empty name. It returns the enclosure as ListEnclosures would
// show it, or with only the name filled in when nothing reports it now.
func (s *Store) NameEnclosure(ctx context.Context, ref, name, note, actor string) (Enclosure, error) {
	key, err := s.ResolveEnclosure(ctx, ref)
	if err != nil {
		return Enclosure{}, err
	}
	name, note = strings.TrimSpace(name), strings.TrimSpace(note)
	if name == "" {
		_, err = s.db.ExecContext(ctx, `DELETE FROM enclosure_name WHERE enclosure = ?`, key)
	} else {
		_, err = s.db.ExecContext(ctx, `INSERT INTO enclosure_name (enclosure, name, note, set_by, set_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (enclosure) DO UPDATE SET name = excluded.name, note = excluded.note, set_by = excluded.set_by, set_at = excluded.set_at`,
			key, name, note, actor, s.now().Unix())
	}
	if err != nil {
		return Enclosure{}, err
	}
	all, err := s.ListEnclosures(ctx)
	if err != nil {
		return Enclosure{}, err
	}
	for _, e := range all {
		if e.Key == key {
			return e, nil
		}
	}
	return Enclosure{Key: key, Name: name, Note: note}, nil
}

// EnclosureNames returns every name a person has given, by key.
func (s *Store) EnclosureNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT enclosure, name FROM enclosure_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, n string
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}
