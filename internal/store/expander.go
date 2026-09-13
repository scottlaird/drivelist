package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Expander is one SAS expander (a drive shelf or backplane) on one host,
// as the open placements see it.
type Expander struct {
	Key       string // what placements carry: the SAS address, or the kernel name from agents that sent none
	Hostname  string
	Dev       string // the kernel's current name
	Product   string // vendor and product from the SAS topology, "" if the host has not reported one
	Name      string // what a person called it
	Note      string
	Drives    int
	FirstSeen time.Time
	LastSeen  time.Time
}

const expanderSelect = `
	SELECT p.expander, h.hostname, MAX(p.expander_dev), COALESCE(n.name, ''), COALESCE(n.note, ''), COUNT(*), MIN(p.first_seen), MAX(p.last_seen)
	FROM placement p JOIN host h USING (host_id) LEFT JOIN expander_name n ON n.expander = p.expander
	WHERE p.ended_at IS NULL AND p.expander != ''`

// ListExpanders returns every expander with a drive currently placed on
// it or present in the host's SAS topology, by host. An expander with no
// drives (a cascaded one, an empty shelf) is only known from the
// topology; the address is the same key in both, so the two views join.
func (s *Store) ListExpanders(ctx context.Context) ([]Expander, error) {
	placed, err := s.expanders(ctx, expanderSelect+` GROUP BY p.expander, p.host_id ORDER BY h.hostname, MAX(p.expander_dev), p.expander`)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.address, h.hostname, n.name, TRIM(n.vendor || ' ' || n.product), COALESCE(x.name, ''), COALESCE(x.note, ''), n.first_seen, n.last_seen
		FROM sas_node n JOIN host h USING (host_id) LEFT JOIN expander_name x ON x.expander = n.address
		WHERE n.gone_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type hostKey struct{ host, key string }
	index := map[hostKey]int{}
	for i, e := range placed {
		index[hostKey{e.Hostname, e.Key}] = i
	}
	out := placed
	for rows.Next() {
		var e Expander
		var first, last int64
		if err := rows.Scan(&e.Key, &e.Hostname, &e.Dev, &e.Product, &e.Name, &e.Note, &first, &last); err != nil {
			return nil, err
		}
		if i, ok := index[hostKey{e.Hostname, e.Key}]; ok {
			out[i].Product = e.Product
			continue
		}
		if !strings.HasPrefix(e.Dev, "expander-") {
			continue // an HBA is listed only when drives sit on its own bays
		}
		e.FirstSeen, e.LastSeen = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Hostname != out[j].Hostname {
			return out[i].Hostname < out[j].Hostname
		}
		return out[i].Dev < out[j].Dev
	})
	return out, nil
}

func (s *Store) expanders(ctx context.Context, query string, args ...any) ([]Expander, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Expander
	for rows.Next() {
		var e Expander
		var first, last int64
		if err := rows.Scan(&e.Key, &e.Hostname, &e.Dev, &e.Name, &e.Note, &e.Drives, &first, &last); err != nil {
			return nil, err
		}
		e.FirstSeen, e.LastSeen = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// ResolveExpander finds an expander by its key, by an unambiguous part of
// the key (case-insensitively, with or without 0x), or by the kernel's
// name when only one host currently has an expander of that name.
func (s *Store) ResolveExpander(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("expander reference is empty: %w", ErrNotFound)
	}
	all, err := s.ListExpanders(ctx)
	if err != nil {
		return "", err
	}
	lower := strings.ToLower(ref)
	var exact, partial, byDev []Expander
	for _, e := range all {
		k := strings.ToLower(e.Key)
		switch {
		case k == lower || k == "0x"+lower:
			exact = append(exact, e)
		case strings.Contains(k, lower):
			partial = append(partial, e)
		case e.Dev == ref:
			byDev = append(byDev, e)
		}
	}
	pick := exact
	if len(pick) == 0 {
		pick = partial
	}
	if len(pick) == 0 {
		pick = byDev
	}
	keys := map[string]bool{}
	for _, e := range pick {
		keys[e.Key] = true
	}
	switch len(keys) {
	case 0:
		// A name given earlier still resolves once nothing is placed on it.
		var key string
		err := s.db.QueryRowContext(ctx, `SELECT expander FROM expander_name WHERE expander = ? OR lower(expander) = ? OR lower(expander) = '0x' || ?`, ref, lower, lower).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("expander %q: %w", ref, ErrNotFound)
		}
		return key, err
	case 1:
		return pick[0].Key, nil
	}
	names := make([]string, 0, len(pick))
	for _, e := range pick {
		names = append(names, fmt.Sprintf("%s (%s on %s)", e.Key, e.Dev, e.Hostname))
	}
	return "", &AmbiguousError{Ref: ref, Candidates: names}
}

// NameExpander records what a person calls an expander, or clears it with
// an empty name. It returns the expander as ListExpanders would show it,
// or with only the name filled in when nothing is placed on it now.
func (s *Store) NameExpander(ctx context.Context, ref, name, note, actor string) (Expander, error) {
	key, err := s.ResolveExpander(ctx, ref)
	if err != nil {
		return Expander{}, err
	}
	name, note = strings.TrimSpace(name), strings.TrimSpace(note)
	if name == "" {
		_, err = s.db.ExecContext(ctx, `DELETE FROM expander_name WHERE expander = ?`, key)
	} else {
		_, err = s.db.ExecContext(ctx, `INSERT INTO expander_name (expander, name, note, set_by, set_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (expander) DO UPDATE SET name = excluded.name, note = excluded.note, set_by = excluded.set_by, set_at = excluded.set_at`,
			key, name, note, actor, s.now().Unix())
	}
	if err != nil {
		return Expander{}, err
	}
	all, err := s.ListExpanders(ctx)
	if err != nil {
		return Expander{}, err
	}
	for _, e := range all {
		if e.Key == key {
			return e, nil
		}
	}
	return Expander{Key: key, Name: name, Note: note}, nil
}

// ExpanderNames returns every name a person has given, by key.
func (s *Store) ExpanderNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT expander, name FROM expander_name`)
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
