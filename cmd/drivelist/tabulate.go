package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// col is one column of a listing: its name for --fields and --sort, its
// header, how to print it, and what to sort by when that is not the
// printed text (a byte count, a time). extra columns are printed only
// when asked for by name or with --allfields.
type col[T any] struct {
	name   string
	header string
	value  func(T) string
	key    func(T) any
	extra  bool
}

// tableOpts are the flags every listing takes.
type tableOpts struct {
	fields string
	all    bool
	sort   string
}

// addTableFlags gives a listing command --fields, --allfields and --sort,
// with the column names in the help text.
func addTableFlags[T any](cmd *cobra.Command, o *tableOpts, cols []col[T]) {
	var def, all []string
	for _, c := range cols {
		all = append(all, c.name)
		if !c.extra {
			def = append(def, c.name)
		}
	}
	f := cmd.Flags()
	f.StringVar(&o.fields, "fields", "", "columns to show, comma-separated (default "+strings.Join(def, ",")+"; all: "+strings.Join(all, ",")+")")
	f.BoolVar(&o.all, "allfields", false, "show every column")
	f.StringVar(&o.sort, "sort", "", "sort by these columns, comma-separated; a leading - sorts descending (e.g. -size,host)")
}

// printTable prints rows as a tab-aligned table with the columns and
// order the options ask for.
func printTable[T any](w io.Writer, o tableOpts, cols []col[T], rows []T) error {
	byName := map[string]col[T]{}
	for _, c := range cols {
		byName[c.name] = c
	}
	var chosen []col[T]
	switch {
	case o.all:
		chosen = cols
	case o.fields != "":
		for _, name := range strings.Split(o.fields, ",") {
			c, ok := byName[strings.TrimSpace(name)]
			if !ok {
				return fmt.Errorf("unknown column %q (valid: %s)", name, colNames(cols))
			}
			chosen = append(chosen, c)
		}
	default:
		for _, c := range cols {
			if !c.extra {
				chosen = append(chosen, c)
			}
		}
	}
	if o.sort != "" {
		type sortKey struct {
			c    col[T]
			desc bool
		}
		var keys []sortKey
		for _, name := range strings.Split(o.sort, ",") {
			name = strings.TrimSpace(name)
			desc := strings.HasPrefix(name, "-")
			c, ok := byName[strings.TrimPrefix(name, "-")]
			if !ok {
				return fmt.Errorf("unknown sort column %q (valid: %s)", name, colNames(cols))
			}
			keys = append(keys, sortKey{c, desc})
		}
		keyOf := func(c col[T], r T) any {
			if c.key != nil {
				return c.key(r)
			}
			return c.value(r)
		}
		sort.SliceStable(rows, func(i, j int) bool {
			for _, k := range keys {
				a, b := keyOf(k.c, rows[i]), keyOf(k.c, rows[j])
				// Unknown values go last whichever way the column sorts.
				if ma, mb := keyMissing(a), keyMissing(b); ma || mb {
					if ma && mb {
						continue
					}
					return mb
				}
				cmp := compareKeys(a, b)
				if cmp == 0 {
					continue
				}
				if k.desc {
					return cmp > 0
				}
				return cmp < 0
			}
			return false
		})
	}
	tw := tab(w)
	for i, c := range chosen {
		if i > 0 {
			fmt.Fprint(tw, "\t")
		}
		fmt.Fprint(tw, c.header)
	}
	fmt.Fprintln(tw)
	for _, r := range rows {
		for i, c := range chosen {
			if i > 0 {
				fmt.Fprint(tw, "\t")
			}
			fmt.Fprint(tw, c.value(r))
		}
		fmt.Fprintln(tw)
	}
	return tw.Flush()
}

func colNames[T any](cols []col[T]) string {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.name
	}
	return strings.Join(names, ",")
}

// compareKeys orders sort keys of the kinds columns produce. Missing
// values (nil, "-", "") sort after everything.
func compareKeys(a, b any) int {
	na, nb := keyMissing(a), keyMissing(b)
	switch {
	case na && nb:
		return 0
	case na:
		return 1
	case nb:
		return -1
	}
	switch x := a.(type) {
	case string:
		y, _ := b.(string)
		return strings.Compare(strings.ToLower(x), strings.ToLower(y))
	case float64:
		y := toFloat(b)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	case time.Time:
		y, _ := b.(time.Time)
		return x.Compare(y)
	case bool:
		y, _ := b.(bool)
		switch {
		case x == y:
			return 0
		case x: // true first
			return -1
		}
		return 1
	}
	if fa, fb := toFloat(a), toFloat(b); fa != fb {
		if fa < fb {
			return -1
		}
		return 1
	}
	return 0
}

func keyMissing(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == "" || x == "-"
	case *timestamppb.Timestamp:
		return x == nil
	case time.Time:
		return x.IsZero()
	}
	return false
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case uint32:
		return float64(x)
	case uint64:
		return float64(x)
	case float64:
		return x
	case *uint64:
		if x != nil {
			return float64(*x)
		}
	case *uint32:
		if x != nil {
			return float64(*x)
		}
	case *int32:
		if x != nil {
			return float64(*x)
		}
	case *timestamppb.Timestamp:
		if x != nil {
			return float64(x.AsTime().Unix())
		}
	}
	return 0
}

// tsKey turns a timestamp into a sort key, missing when unset.
func tsKey(t *timestamppb.Timestamp) any {
	if t == nil {
		return nil
	}
	return t.AsTime()
}

// optKey turns an optional counter into a sort key, missing when unset.
func optKey(p *uint64) any {
	if p == nil {
		return nil
	}
	return float64(*p)
}
