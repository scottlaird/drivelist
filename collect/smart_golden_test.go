package collect

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// smartGolden is one device's SMART result as captured on a real host.
type smartGolden struct {
	Device  string
	Skipped string        `json:",omitempty"`
	Summary *SmartSummary `json:",omitempty"`
}

// TestCapturedSmart runs the parser over every smartctl document a capture
// recorded and compares the summaries with the fixture's smart.json golden
// (regenerate with -update). Captures without smartctl output are skipped.
func TestCapturedSmart(t *testing.T) {
	for _, name := range capturedFixtures {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join("testdata", name)
			files, _ := filepath.Glob(filepath.Join(root, "exec", "smartctl -j -a -n standby _dev_*"))
			if len(files) == 0 {
				t.Skip("no smartctl output in this capture")
			}
			var got []smartGolden
			for _, f := range files {
				dev := strings.TrimPrefix(filepath.Base(f), "smartctl -j -a -n standby _dev_")
				data, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				run := func(context.Context, ...string) ([]byte, int, error) {
					var doc struct {
						Smartctl struct {
							ExitStatus int `json:"exit_status"`
						} `json:"smartctl"`
					}
					json.Unmarshal(data, &doc)
					return data, doc.Smartctl.ExitStatus, nil
				}
				s, _ := SampleSmart(context.Background(), run, dev, "scsi", time.Time{})
				got = append(got, smartGolden{Device: dev, Skipped: s.Skipped, Summary: s.Summary})
			}
			sort.Slice(got, func(i, j int) bool { return got[i].Device < got[j].Device })

			goldenPath := filepath.Join(root, "smart.json")
			if *update {
				data, _ := json.MarshalIndent(got, "", "  ")
				if err := os.WriteFile(goldenPath, append(data, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			gotJSON, _ := json.MarshalIndent(got, "", "  ")
			if string(want) != string(gotJSON)+"\n" {
				t.Errorf("SMART summaries differ from %s; run with -update if the change is intended", goldenPath)
			}
			// Every real document must parse into a summary unless it is a
			// skip the sampler recognises.
			for _, g := range got {
				if g.Summary == nil && g.Skipped != SkipStandby && g.Skipped != SkipUnsupported {
					t.Errorf("%s: no summary and unexpected skip %q", g.Device, g.Skipped)
				}
			}
		})
	}
}
