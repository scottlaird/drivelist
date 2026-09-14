package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

type trow struct {
	name string
	size uint64
	when *timestamppb.Timestamp
	hot  *uint64
}

var tcols = []col[trow]{
	{name: "name", header: "NAME", value: func(r trow) string { return r.name }},
	{name: "size", header: "SIZE", value: func(r trow) string { return size(r.size) }, key: func(r trow) any { return r.size }},
	{name: "when", header: "WHEN", value: func(r trow) string { return when(r.when) }, key: func(r trow) any { return tsKey(r.when) }},
	{name: "hot", header: "HOT", value: func(r trow) string { return optU(r.hot) }, key: func(r trow) any { return optKey(r.hot) }, extra: true},
}

func TestPrintTable(t *testing.T) {
	t0 := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	seven := uint64(7)
	rows := []trow{
		{"b", 2e12, timestamppb.New(t0.Add(time.Hour)), nil},
		{"A", 8e12, timestamppb.New(t0), &seven},
		{"c", 1e12, nil, nil},
	}
	run := func(o tableOpts) (string, error) {
		var buf bytes.Buffer
		err := printTable(&buf, o, tcols, append([]trow(nil), rows...))
		return buf.String(), err
	}
	out, err := run(tableOpts{})
	if err != nil || !strings.HasPrefix(out, "NAME") || strings.Contains(out, "HOT") || strings.Count(out, "\n") != 4 {
		t.Errorf("default columns:\n%s%v", out, err)
	}
	out, _ = run(tableOpts{all: true})
	if !strings.Contains(out, "HOT") {
		t.Errorf("--allfields lacks the extra column:\n%s", out)
	}
	out, _ = run(tableOpts{fields: "hot,name"})
	if !strings.HasPrefix(out, "HOT") || !strings.Contains(out, "7") {
		t.Errorf("--fields order:\n%s", out)
	}
	if _, err := run(tableOpts{fields: "nope"}); err == nil || !strings.Contains(err.Error(), "valid: name,size,when,hot") {
		t.Errorf("unknown field: %v", err)
	}
	// Sizes sort as numbers (8 TB after 2 TB after 1 TB), not as text.
	out, _ = run(tableOpts{sort: "size"})
	if lines := strings.Split(strings.TrimSpace(out), "\n"); !strings.HasPrefix(lines[1], "c") || !strings.HasPrefix(lines[3], "A") {
		t.Errorf("sort by size:\n%s", out)
	}
	out, _ = run(tableOpts{sort: "-size"})
	if lines := strings.Split(strings.TrimSpace(out), "\n"); !strings.HasPrefix(lines[1], "A") {
		t.Errorf("sort by -size:\n%s", out)
	}
	// Names sort case-insensitively; missing times sort last.
	out, _ = run(tableOpts{sort: "name"})
	if lines := strings.Split(strings.TrimSpace(out), "\n"); !strings.HasPrefix(lines[1], "A") || !strings.HasPrefix(lines[2], "b") {
		t.Errorf("sort by name:\n%s", out)
	}
	out, _ = run(tableOpts{sort: "when"})
	if lines := strings.Split(strings.TrimSpace(out), "\n"); !strings.HasPrefix(lines[1], "A") || !strings.HasPrefix(lines[3], "c") {
		t.Errorf("sort by when (missing last):\n%s", out)
	}
	out, _ = run(tableOpts{sort: "-hot,name"})
	if lines := strings.Split(strings.TrimSpace(out), "\n"); !strings.HasPrefix(lines[1], "A") || !strings.HasPrefix(lines[2], "b") {
		t.Errorf("sort by -hot then name:\n%s", out)
	}
	if _, err := run(tableOpts{sort: "-nope"}); err == nil {
		t.Error("unknown sort column accepted")
	}
}
