package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scottlaird/drivelist/collect"
)

// useFixture points the listing at the synthetic fixture for one test.
func useFixture(t *testing.T) {
	t.Helper()
	saved, savedSAS := collectAll, collectSAS
	fx := collect.Fixture(filepath.Join("..", "..", "collect", "testdata", "synthetic"))
	collectAll, collectSAS = fx.Collect, fx.SAS
	t.Cleanup(func() { collectAll, collectSAS = saved, savedSAS })
}

func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

func TestListDefault(t *testing.T) {
	useFixture(t)
	out, errOut, err := run(t)
	if err != nil {
		t.Fatalf("drivelist: %v", err)
	}
	if !strings.HasPrefix(out, "Device Name\t") {
		t.Errorf("output does not start with the default header:\n%s", out)
	}
	for _, want := range []string{"sda", "7SG3RM2G", "nvme0n1", "8 TB"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "sdd could not be identified") {
		t.Errorf("stderr lacks the degraded-device note:\n%s", errOut)
	}
}

func TestListUnusedFields(t *testing.T) {
	useFixture(t)
	out, _, err := run(t, "--unused", "--fields=devicename,serial")
	if err != nil {
		t.Fatalf("drivelist: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// header, underline, then sda (the fixture has no zpool output, so its
	// pool disk is unused here), sdc (unused SSD), sdd (unidentified).
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5:\n%s", len(lines), out)
	}
	for i, want := range []string{"sda", "sdc", "sdd"} {
		if got, _, _ := strings.Cut(lines[i+2], "\t"); got != want {
			t.Errorf("unused row %d = %q, want %s", i, lines[i+2], want)
		}
	}
	if strings.Contains(out, "Model") {
		t.Errorf("--fields did not narrow the columns:\n%s", out)
	}
}

func TestListLedctl(t *testing.T) {
	useFixture(t)
	out, _, err := run(t, "--unused", "--ledctl=locate")
	if err != nil {
		t.Fatalf("drivelist: %v", err)
	}
	if got, want := strings.TrimSpace(out), "ledctl locate=sda,sdc,sdd"; got != want {
		t.Errorf("ledctl output = %q, want %q", got, want)
	}
}

func TestListBadField(t *testing.T) {
	useFixture(t)
	if _, _, err := run(t, "--fields=nosuch"); err == nil {
		t.Error("--fields=nosuch: got nil error, want error")
	}
}

func TestCaptureArgs(t *testing.T) {
	if _, _, err := run(t, "capture"); err == nil {
		t.Error("capture with no DIR: got nil error, want error")
	}
}
