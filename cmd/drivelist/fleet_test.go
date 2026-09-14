package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scottlaird/drivelist/internal/server"
	"github.com/scottlaird/drivelist/internal/store"
)

// fleetEnv starts a real server over a temp store and points the CLI at
// it through the environment, with the synthetic fixture as this host.
func fleetEnv(t *testing.T) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := server.New(st, server.Config{AgentToken: "a", OperatorToken: "o", Interval: time.Minute}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	t.Setenv("DRIVELIST_SERVER", hs.URL)
	t.Setenv("DRIVELIST_AGENT_TOKEN", "a")
	t.Setenv("DRIVELIST_OPERATOR_TOKEN", "o")
	t.Setenv("DRIVELIST_ACTOR", "tester@here")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no real config file
	useFixture(t)
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, errOut, err := run(t, args...)
	if err != nil {
		t.Fatalf("drivelist %s: %v\nstderr: %s", strings.Join(args, " "), err, errOut)
	}
	return out
}

func TestFleetEndToEnd(t *testing.T) {
	fleetEnv(t)

	out := mustRun(t, "report")
	if !strings.Contains(out, "reported 5 devices") || !strings.Contains(out, "inventory changed") {
		t.Errorf("report: %q", out)
	}
	out = mustRun(t, "report")
	if !strings.Contains(out, "no change") {
		t.Errorf("second report: %q", out)
	}

	out = mustRun(t, "hosts")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "just now") || !strings.Contains(lines[1], "  ok") {
		t.Errorf("hosts: %q", out)
	}
	// The agent's version travels with every report; a test build is "dev".
	if !strings.Contains(lines[0], "AGENT") || !strings.HasSuffix(strings.TrimSpace(lines[1]), "dev") {
		t.Errorf("hosts lacks the agent version:\n%s", out)
	}
	if !strings.Contains(mustRun(t, "hosts", "--ids"), "MACHINE ID") {
		t.Error("hosts --ids lacks the id column")
	}

	out = mustRun(t, "drives")
	for _, want := range []string{"7SG3RM2G", "VLG32AEY", "S5GXNX0T123456B", "expander-4:0 bay 0", "mount > /backup", "SATA", "NVMe"} {
		if !strings.Contains(out, want) {
			t.Errorf("drives lacks %q:\n%s", want, out)
		}
	}
	out = mustRun(t, "enclosures")
	if !strings.Contains(out, "expander-4:0") || !strings.Contains(out, "0x500605b00a1b2c3e") || !strings.Contains(out, "LSI SAS2X36") {
		t.Errorf("enclosures:\n%s", out)
	}
	out = mustRun(t, "enclosure", "expander-4:0", "name", "front shelf", "--note", "by the door")
	if !strings.Contains(out, `0x500605b00a1b2c3e (expander-4:0 on `) || !strings.Contains(out, `named "front shelf"`) {
		t.Errorf("enclosure name: %q", out)
	}
	if out = mustRun(t, "drives"); !strings.Contains(out, "front shelf bay 0") || strings.Contains(out, "expander-4:0 bay 0") {
		t.Errorf("drives after naming:\n%s", out)
	}
	if out = mustRun(t, "enclosures"); !strings.Contains(out, "front shelf") || !strings.Contains(out, "by the door") {
		t.Errorf("enclosures after naming:\n%s", out)
	}
	if out = mustRun(t, "events"); !strings.Contains(out, "first seen    ") || !strings.Contains(out, "front shelf bay") {
		t.Errorf("events use the enclosure name:\n%s", out)
	}
	if _, _, err := run(t, "enclosure", "nosuch", "name", "x"); err == nil {
		t.Error("naming an unknown enclosure succeeded")
	}

	out = mustRun(t, "drives", "--unused")
	if strings.Contains(out, "VLG32AEY") || !strings.Contains(out, "S5RRNF0R123456A") {
		t.Errorf("drives --unused:\n%s", out)
	}

	out = mustRun(t, "drive", "VLG32AEY")
	if !strings.Contains(out, "serial VLG32AEY") || !strings.Contains(out, "status: ok") || !strings.Contains(out, "now: ") || !strings.Contains(out, "keys: serial:HUH728080ALE601/VLG32AEY, wwn:0x5000cca260c165e2") {
		t.Errorf("drive:\n%s", out)
	}
	out = mustRun(t, "drive", "0x5000cca260c165e2", "history")
	if !strings.Contains(out, "first seen    ") || !strings.Contains(out, "\nstill present on ") {
		t.Errorf("history:\n%s", out)
	}

	out = mustRun(t, "drive", "VLG32AEY", "mark", "suspect", "--note", "r_await 3x siblings")
	if !strings.Contains(out, "status        suspect  tester@here: \"r_await 3x siblings\"") {
		t.Errorf("mark:\n%s", out)
	}
	out = mustRun(t, "drive", "VLG32AEY", "note", "RMA", "4471", "opened")
	if !strings.Contains(out, `note          tester@here: "RMA 4471 opened"`) {
		t.Errorf("note:\n%s", out)
	}
	out = mustRun(t, "drive", "VLG32AEY")
	if !strings.Contains(out, "status: suspect (tester@here") {
		t.Errorf("drive after mark:\n%s", out)
	}
	if !strings.Contains(mustRun(t, "drives", "--status", "suspect"), "VLG32AEY") {
		t.Error("drives --status suspect did not list the marked drive")
	}

	out = mustRun(t, "events", "--kind", "status_changed,note")
	if strings.Count(out, "\n") != 3 {
		t.Errorf("events: want header + 2 rows:\n%s", out)
	}
	out = mustRun(t, "missing")
	if !strings.Contains(out, "SERIAL") || strings.Contains(out, "VLG32AEY") {
		t.Errorf("missing:\n%s", out)
	}

	if _, _, err := run(t, "drive", "nosuch"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown drive: %v", err)
	}
	if _, _, err := run(t, "drive", "VLG32AEY", "mark", "broken"); err == nil {
		t.Error("bad status accepted")
	}
	if _, _, err := run(t, "drive", "VLG32AEY", "dance"); err == nil {
		t.Error("unknown action accepted")
	}
	out = mustRun(t, "drive", "VLG32AEY", "kernel")
	if !strings.Contains(out, "no kernel log lines") {
		t.Errorf("kernel with nothing recorded:\n%s", out)
	}
	out = mustRun(t, "drive", "VLG32AEY", "smart")
	if !strings.Contains(out, "no SMART samples") {
		t.Errorf("smart with nothing recorded:\n%s", out)
	}
	out = mustRun(t, "drive", "VLG32AEY", "io")
	if !strings.Contains(out, "no I/O samples") {
		t.Errorf("io with nothing recorded:\n%s", out)
	}
	out = mustRun(t, "io", "compare")
	if !strings.Contains(out, "no I/O samples") {
		t.Errorf("io compare with nothing recorded:\n%s", out)
	}
	if got := groupLabel("zfs > space 5925914041408872576 > raidz2 12372305547527317295", 8); got != "space/raidz2 …7295 (8)" {
		t.Errorf("groupLabel = %q", got)
	}
	if _, _, err := run(t, "drive", "VLG32AEY", "merge", "VLG32AEY"); err == nil || !strings.Contains(err.Error(), "same drive") {
		t.Errorf("merge into itself: %v", err)
	}
	out = mustRun(t, "drive", "VLG32AEY", "merge", "7SG3RM2G")
	if !strings.Contains(out, "merged        record 7SG3RM2G") {
		t.Errorf("merge:\n%s", out)
	}
	if _, _, err := run(t, "drive", "7SG3RM2G"); err != nil {
		t.Errorf("merged serial no longer resolves: %v", err)
	}
	if _, _, err := run(t, "host", "merge", "nosuch", "other"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("host merge with unknown hosts: %v", err)
	}
	out = mustRun(t, "admin", "rebuild")
	if !strings.HasPrefix(out, "rebuilt from 1 snapshots: 4 placements") {
		t.Errorf("admin rebuild:\n%s", out)
	}
	out = mustRun(t, "drive", "VLG32AEY", "history")
	if !strings.Contains(out, "first seen    ") || !strings.Contains(out, "\nstill present on ") {
		t.Errorf("history after rebuild:\n%s", out)
	}
	if _, _, err := run(t, "drive", "VLG32AEY", "smart", "--raw"); err == nil {
		t.Error("smart --raw with nothing stored succeeded")
	}
	if !strings.HasPrefix(strings.TrimSpace(mustRun(t, "hosts", "--json")), "{") {
		t.Error("--json did not print JSON")
	}
}

func TestOlderServer(t *testing.T) {
	// A server with no handler for a call answers 404 at the HTTP layer.
	hs := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(hs.Close)
	t.Setenv("DRIVELIST_SERVER", hs.URL)
	t.Setenv("DRIVELIST_OPERATOR_TOKEN", "o")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, _, err := run(t, "io", "compare")
	if err == nil || !strings.Contains(err.Error(), "older than this client") {
		t.Errorf("old server: %v", err)
	}
}

func TestFleetNeedsServer(t *testing.T) {
	t.Setenv("DRIVELIST_SERVER", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, _, err := run(t, "hosts"); err == nil || !strings.Contains(err.Error(), "no server") {
		t.Errorf("hosts without a server: %v", err)
	}
	t.Setenv("DRIVELIST_SERVER", "localhost:1")
	t.Setenv("DRIVELIST_OPERATOR_TOKEN", "")
	if _, _, err := run(t, "hosts"); err == nil || !strings.Contains(err.Error(), "no operator token") {
		t.Errorf("hosts without a token: %v", err)
	}
}

func TestConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := writeFile(filepath.Join(dir, "drivelist", "config"), "# comment\nserver = fleet.example:9450\noperator_token = abc\nactor = scott@laptop\n"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRIVELIST_SERVER", "")
	t.Setenv("DRIVELIST_OPERATOR_TOKEN", "")
	t.Setenv("DRIVELIST_ACTOR", "")
	cfg := &clientConfig{}
	cfg.resolve()
	if cfg.server != "fleet.example:9450" || cfg.operatorToken != "abc" || cfg.actor != "scott@laptop" {
		t.Errorf("config = %+v", cfg)
	}
	url, err := cfg.baseURL()
	if err != nil || url != "http://fleet.example:9450" {
		t.Errorf("baseURL = %q, %v", url, err)
	}
	t.Setenv("DRIVELIST_SERVER", "https://other:1")
	cfg = &clientConfig{}
	cfg.resolve()
	if cfg.server != "https://other:1" {
		t.Errorf("env did not beat the file: %q", cfg.server)
	}

	// A token file, as the systemd units use.
	tokenFile := filepath.Join(dir, "agent-token")
	if err := os.WriteFile(tokenFile, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRIVELIST_AGENT_TOKEN", "")
	t.Setenv("DRIVELIST_AGENT_TOKEN_FILE", tokenFile)
	cfg = &clientConfig{}
	cfg.resolve()
	if cfg.agentToken != "file-secret" {
		t.Errorf("agent token from file = %q", cfg.agentToken)
	}
}

func writeFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

func TestHardwareCommands(t *testing.T) {
	fleetEnv(t)
	out := mustRun(t, "hardware")
	for _, want := range []string{"RS500A-E10-RS12U", "HGST 4U60_STOR_ENCL", "2x6", "5x12"} {
		if !strings.Contains(out, want) {
			t.Errorf("hardware lacks %q:\n%s", want, out)
		}
	}
	mustRun(t, "report")
	// The synthetic shelf has no profile: bays are as the firmware names them.
	out = mustRun(t, "enclosure", "expander-4:0", "bays")
	if !strings.Contains(out, "no hardware profile") || !strings.Contains(out, "sas:0\tsda") && !strings.Contains(out, "sas:0") || !strings.Contains(out, "7SG3RM2G") {
		t.Errorf("bays without profile:\n%s", out)
	}
	host := mustRun(t, "hosts")
	hostname := strings.Fields(strings.Split(strings.TrimSpace(host), "\n")[1])[0]
	out = mustRun(t, "hardware", "check", hostname)
	if !strings.Contains(out, "no profile for model") || !strings.Contains(out, "sas:0 holds sda") {
		t.Errorf("hardware check:\n%s", out)
	}
	if _, _, err := run(t, "hardware", "check", "nosuchhost"); err == nil {
		t.Error("check of unknown host succeeded")
	}
}

func TestSmartList(t *testing.T) {
	fleetEnv(t)
	mustRun(t, "report")
	out := mustRun(t, "smart")
	if !strings.Contains(out, "HEALTH") || !strings.Contains(out, "no reading") || !strings.Contains(out, "VLG32AEY") {
		t.Errorf("smart:\n%s", out)
	}
	if out := mustRun(t, "smart", "--problems"); !strings.Contains(out, "no drive's SMART reading reports a problem") {
		t.Errorf("smart --problems: %q", out)
	}
}
