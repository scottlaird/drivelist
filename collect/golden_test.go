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

var update = flag.Bool("update", false, "rewrite the golden files (inventory.json, smart.json, sas.json) in each captured fixture")

// capturedFixtures are trees recorded from real hosts with --capture. Each
// carries an inventory.json golden file of what Collect produces from it;
// regenerate with `go test ./collect -update` and review the diff.
var capturedFixtures = []string{"fs2", "mgmt1", "pbs1", "mac"}

func TestCapturedFixtures(t *testing.T) {
	for _, name := range capturedFixtures {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join("testdata", name)
			inv, err := Fixture(root).Collect()
			if err != nil {
				t.Fatalf("Collect: %v", err)
			}
			got := golden{Devices: forGolden(inv.Devices), Unmapped: inv.Unmapped}

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
			var want golden
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatalf("parsing %s: %v", goldenPath, err)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("inventory differs from %s (-want +got); run with -update if the change is intended:\n%s", goldenPath, diff)
			}
		})
	}
}

// TestCapturedSAS checks each captured fixture that has a SAS transport
// class against its sas.json golden, the topology SAS produces from the
// tree. Fixtures captured before the SAS trees were recorded, and hosts
// without SAS, have neither and are skipped.
func TestCapturedSAS(t *testing.T) {
	for _, name := range capturedFixtures {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join("testdata", name)
			if _, err := os.Stat(filepath.Join(root, "sys", "class", "sas_host")); err != nil {
				t.Skip("no SAS transport in this capture")
			}
			got, err := Fixture(root).SAS()
			if err != nil {
				t.Fatalf("SAS: %v", err)
			}
			for _, n := range got.Nodes {
				n.Path = "" // the fixture directory, not part of the topology
			}
			goldenPath := filepath.Join(root, "sas.json")
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
			var want SASTopology
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatalf("parsing %s: %v", goldenPath, err)
			}
			if diff := cmp.Diff(&want, got); diff != "" {
				t.Errorf("topology differs from %s (-want +got); run with -update if the change is intended:\n%s", goldenPath, diff)
			}
		})
	}
}

// golden is the shape of inventory.json.
type golden struct {
	Devices  []*drivelist.Device
	Unmapped []drivelist.PoolMember `json:",omitempty"`
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
// of them was identified, that every drive behind an expander reports a
// bay, and that pool membership comes out exactly as the libzfs
// implementation printed it.
func TestFS2Shape(t *testing.T) {
	inv, err := Fixture(filepath.Join("testdata", "fs2")).Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var drives, emptyBays, inExpander, withBay, inPool int
	bySerial := make(map[string]*drivelist.Device)
	for _, d := range inv.Devices {
		switch {
		case d.IsEmptyBay():
			emptyBays++
		default:
			drives++
			bySerial[d.Serial] = d
			if d.Expander != "" {
				inExpander++
				if d.EnclosureBay != "" {
					withBay++
				}
			}
			if d.MemberState != "" {
				inPool++
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
	if inPool != 88 {
		t.Errorf("drives in a pool = %d, want 88", inPool)
	}

	// Two drives whose use strings the README recorded from the libzfs
	// implementation, and whose vdevs still exist. The zpool implementation
	// must reproduce them byte for byte.
	libzfsRef := map[string]string{
		"001619PJLREV_VKJJLREV": "zfs > space 5925914041408872576 > raidz2 5236016460003016805 > disk 9810514795403010748",
		"VJG24UZX":              "zfs > space 5925914041408872576 > raidz2 12372305547527317295 > disk 4484622911110778645",
	}
	for serial, want := range libzfsRef {
		d := bySerial[serial]
		if d == nil {
			t.Errorf("serial %s not in inventory", serial)
			continue
		}
		if len(d.Uses) != 1 || d.Uses[0] != want {
			t.Errorf("serial %s uses = %q, want [%q]", serial, d.Uses, want)
		}
	}

	// backups has a REMOVED member under spare-6 that no present device
	// matches: the fleet design's "installed but invisible" case.
	want := []drivelist.PoolMember{{
		Pool:  "backups",
		Path:  "/dev/disk/by-id/scsi-35000cca25492cd80-part1",
		GUID:  "1082024170414391897",
		State: "REMOVED",
	}}
	if diff := cmp.Diff(want, inv.Unmapped); diff != "" {
		t.Errorf("Unmapped mismatch (-want +got):\n%s", diff)
	}
	t.Logf("fs2: %d drives, %d behind expanders, %d in pools, %d empty bays", drives, inExpander, inPool, emptyBays)
}
