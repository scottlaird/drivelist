package agent

import (
	"sort"
	"time"

	"github.com/scottlaird/drivelist"
	pb "github.com/scottlaird/drivelist/internal/pb/drivelistv1"
	"github.com/scottlaird/drivelist/internal/report"
)

// identityMap remembers which drive each kernel device name and SCSI
// address referred to, from recent inventories. Entries outlive the device
// by a grace period, so the messages a drive logs on its way out still
// attach to it.
type identityMap struct {
	byName map[string]idEntry
	byAddr map[string]idEntry
}

type idEntry struct {
	id   *pb.DriveIdentity
	seen time.Time
}

const identityGrace = 10 * time.Minute

func newIdentityMap() identityMap {
	return identityMap{byName: map[string]idEntry{}, byAddr: map[string]idEntry{}}
}

// update records the inventory's devices and returns the names that were
// not known before.
func (m identityMap) update(inv *drivelist.Inventory, now time.Time) (appeared []string) {
	if inv != nil {
		for _, d := range inv.Devices {
			if d.DeviceName == "" || d.Error != "" || (d.WWN == "" && d.Serial == "") {
				continue
			}
			pd := report.Device(d)
			e := idEntry{id: pd.Identity, seen: now}
			if _, known := m.byName[d.DeviceName]; !known {
				appeared = append(appeared, d.DeviceName)
			}
			m.byName[d.DeviceName] = e
			if pd.ScsiAddr != "" {
				m.byAddr[pd.ScsiAddr] = e
			}
		}
	}
	for _, table := range []map[string]idEntry{m.byName, m.byAddr} {
		for k, e := range table {
			if now.Sub(e.seen) > identityGrace {
				delete(table, k)
			}
		}
	}
	return appeared
}

// names lists the current device names, sorted; entries in their grace
// period are included, since they may still answer smartctl.
func (m identityMap) names() []string {
	out := make([]string, 0, len(m.byName))
	for n := range m.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (m identityMap) lookup(devName, addr string) (*pb.DriveIdentity, bool) {
	if e, ok := m.byName[devName]; ok && devName != "" {
		return e.id, true
	}
	if e, ok := m.byAddr[addr]; ok && addr != "" {
		return e.id, true
	}
	return nil, false
}
