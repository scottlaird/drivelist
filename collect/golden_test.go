package collect

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/scottlaird/drivelist"
)

var update = flag.Bool("update", false, "rewrite the inventory.json golden file in each captured fixture")

// capturedFixtures are trees recorded from real hosts with --capture. Each
// carries an inventory.json golden file of what Collect produces from it;
// regenerate with `go test ./collect -update` and review the diff.
var capturedFixtures = []string{"fs2"}

func TestCapturedFixtures(t *testing.T) {
	for _, name := range capturedFixtures {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join("testdata", name)
			inv, err := Fixture(root).Collect()
			if err != nil {
				t.Fatalf("Collect: %v", err)
			}
			got := forGolden(inv.Devices)

			goldenPath := filepath.Join(root, "inventory.json")
			if *update {
				data, err := json.MarshalIndent(got, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, append(data, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			data, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			var want []*drivelist.Device
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatalf("parsing %s: %v", goldenPath, err)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("inventory differs from %s (-want +got); run with -update if the change is intended:\n%s", goldenPath, diff)
			}
		})
	}
}

// forGolden strips the raw udev property map, which is large and already
// covered by the fixture files themselves, and drops nil-vs-empty
// distinctions that JSON cannot preserve.
func forGolden(devices []*drivelist.Device) []*drivelist.Device {
	out := make([]*drivelist.Device, len(devices))
	for i, d := range devices {
		c := *d
		c.Attribs = nil
		if len(c.Uses) == 0 {
			c.Uses = nil
		}
		if len(c.Devices) == 0 {
			c.Devices = nil
		}
		out[i] = &c
	}
	return out
}

// TestFS2Shape checks the facts about fs2 that a reader can verify against
// the capture without the golden file: the number of drives, that every one
// of them was identified, and that every drive behind an expander reports a
// bay.
func TestFS2Shape(t *testing.T) {
	inv, err := Fixture(filepath.Join("testdata", "fs2")).Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var drives, emptyBays, inExpander, withBay int
	for _, d := range inv.Devices {
		switch {
		case d.IsEmptyBay():
			emptyBays++
		default:
			drives++
			if d.Expander != "" {
				inExpander++
				if d.EnclosureBay != "" {
					withBay++
				}
			}
		}
	}
	if drives != 95 {
		t.Errorf("drives = %d, want 95 (one per sys/block entry)", drives)
	}
	if got := inv.Degraded(); len(got) != 0 {
		t.Errorf("Degraded() = %d devices, want 0", len(got))
	}
	if inExpander != withBay {
		t.Errorf("drives in an expander = %d, with a bay = %d; want equal", inExpander, withBay)
	}
	t.Logf("fs2: %d drives, %d behind expanders, %d empty bays", drives, inExpander, emptyBays)
}
