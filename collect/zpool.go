package collect

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os/exec"
	"slices"
	"strings"

	"github.com/scottlaird/drivelist"
)

// This file detects ZFS pool membership by running zpool rather than
// linking libzfs. The use strings it produces are byte-identical to what the
// libzfs path produced, so switching implementations does not look like
// every drive changed use:
//
//	zfs > <pool> <pool guid> > raidz2 <vdev guid> > disk <leaf guid>
//	zfs > <pool> <pool guid> > log > mirror <vdev guid> > disk <leaf guid>
//	zfs > l2arc <pool> > disk <leaf guid>
//	zfs > spare <pool> > disk <leaf guid>
//
// The vdev names are what libzfs's zpool_vdev_name() returns without the
// type-id flag: "raidz2", "mirror", "spare", "replacing", never "raidz2-0".
// The l2arc and spare forms carry the pool name without its GUID; that is
// how go-libzfs's caller built them and it is preserved on purpose.

// zpoolRow is one line of a zpool status config section.
type zpoolRow struct {
	depth int    // 0 for the pool, 1 for top-level vdevs and section headings
	name  string // path with -P, GUID with -g, vdev name like raidz2-0, or a heading
	state string // "" for headings
	extra string // trailing text: "was /dev/…" for a missing leaf, "currently in use"
}

// zpoolStatus is the config section of one pool.
type zpoolStatus struct {
	name string
	rows []zpoolRow
}

// parseZpoolStatus reads the output of zpool status (-P or -g), returning
// each pool's config rows in order. Everything outside the config sections
// is ignored.
func parseZpoolStatus(text string) ([]zpoolStatus, error) {
	var pools []zpoolStatus
	var cur *zpoolStatus
	inConfig := false
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "  pool: "):
			pools = append(pools, zpoolStatus{name: strings.TrimSpace(strings.TrimPrefix(line, "  pool: "))})
			cur = &pools[len(pools)-1]
			inConfig = false
		case strings.HasPrefix(line, "config:"):
			inConfig = true
		case !inConfig:
		case !strings.HasPrefix(line, "\t"):
			if strings.TrimSpace(line) != "" {
				inConfig = false // "errors:" or any other section
			}
		default:
			if cur == nil {
				return nil, fmt.Errorf("zpool status: config row before any pool: %q", line)
			}
			body := strings.TrimPrefix(line, "\t")
			fields := strings.Fields(body)
			if len(fields) == 0 || fields[0] == "NAME" {
				continue
			}
			row := zpoolRow{
				depth: (len(body) - len(strings.TrimLeft(body, " "))) / 2,
				name:  fields[0],
			}
			if len(fields) > 1 {
				row.state = fields[1]
			}
			// Vdev rows carry READ WRITE CKSUM counters after the state;
			// spares rows do not, so anything after the state is extra.
			switch {
			case len(fields) >= 5 && isCounter(fields[2]) && isCounter(fields[3]) && isCounter(fields[4]):
				row.extra = strings.Join(fields[5:], " ")
			case len(fields) > 2:
				row.extra = strings.Join(fields[2:], " ")
			}
			cur.rows = append(cur.rows, row)
		}
	}
	return pools, scanner.Err()
}

func isCounter(s string) bool { return s != "" && s[0] >= '0' && s[0] <= '9' }

// parseZpoolGUIDs reads `zpool get -Hp guid`: one "pool\tguid\tvalue\tsource"
// line per pool.
func parseZpoolGUIDs(text string) map[string]string {
	guids := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		f := strings.Split(line, "\t")
		if len(f) >= 3 && f[1] == "guid" {
			guids[f[0]] = f[2]
		}
	}
	return guids
}

// zpoolMember is one leaf of a pool as libzfs would have described it.
type zpoolMember struct {
	path  string
	guid  string
	state string
	use   string
}

// zpoolSections are the headings zpool status prints, unindented, to group
// the vdevs that follow by allocation class. Regular and special/dedup vdevs are ordinary
// children of the root as far as the use string is concerned.
var zpoolSections = map[string]bool{"logs": true, "cache": true, "spares": true, "special": true, "dedup": true}

// poolMembers zips a pool's -P rows with its -g rows and walks the tree
// once, producing every leaf with the use string libzfs would have given
// it. When one path appears more than once (an in-use spare is listed both
// in the spares section and under its spare-N vdev), the later section
// wins, as it did with the map the libzfs path built.
func poolMembers(pool, guidPool zpoolStatus, poolGUID string) ([]zpoolMember, error) {
	if len(pool.rows) != len(guidPool.rows) {
		return nil, fmt.Errorf("zpool status: pool %s has %d rows with -P but %d with -g", pool.name, len(pool.rows), len(guidPool.rows))
	}
	if len(pool.rows) == 0 || pool.rows[0].depth != 0 {
		return nil, fmt.Errorf("zpool status: pool %s: first config row is not the pool", pool.name)
	}

	// prefix[d] is the use-string prefix for rows at depth d, given the
	// rows above them.
	prefix := []string{"zfs > " + pool.name + " " + poolGUID}
	byPath := make(map[string]int)
	var members []zpoolMember
	section := ""
	for i := 1; i < len(pool.rows); i++ {
		row, grow := pool.rows[i], guidPool.rows[i]
		if row.depth != grow.depth {
			return nil, fmt.Errorf("zpool status: pool %s: row %d is at depth %d with -P but %d with -g", pool.name, i, row.depth, grow.depth)
		}
		if row.depth == 0 {
			if zpoolSections[row.name] && row.state == "" {
				section = row.name
				continue
			}
			return nil, fmt.Errorf("zpool status: pool %s: unexpected unindented row %q", pool.name, row.name)
		}
		prefix = prefix[:row.depth]
		parent := prefix[row.depth-1]
		if row.depth == 1 {
			switch section {
			case "logs":
				parent = prefix[0] + " > log"
			case "cache":
				parent = "zfs > l2arc " + pool.name
			case "spares":
				parent = "zfs > spare " + pool.name
			}
		}
		path, isLeaf := leafPath(row)
		if !isLeaf {
			prefix = append(prefix, parent+" > "+vdevTypeName(row.name)+" "+grow.name)
			continue
		}
		m := zpoolMember{path: path, guid: grow.name, state: row.state, use: parent + " > disk " + grow.name}
		if j, seen := byPath[path]; seen {
			members[j] = m
		} else {
			byPath[path] = len(members)
			members = append(members, m)
		}
	}
	return members, nil
}

// leafPath reports whether a -P row is a leaf and, if so, its device path.
// A missing device is printed as its GUID with "was <path>" after the
// counters; the path is what the inventory can be matched against.
func leafPath(row zpoolRow) (string, bool) {
	if strings.HasPrefix(row.name, "/") {
		return row.name, true
	}
	if was, ok := strings.CutPrefix(row.extra, "was "); ok {
		return was, true
	}
	if isVdevName(row.name) {
		return "", false
	}
	return row.name, true // a bare device name; zpool without -P, or a whole-disk id
}

// isVdevName recognises interior vdevs as zpool status names them:
// <type>-<id>, with draid types carrying a parameter block before the id.
func isVdevName(name string) bool {
	for _, t := range []string{"raidz", "mirror", "spare", "replacing", "indirect", "draid"} {
		if strings.HasPrefix(name, t) && strings.Contains(name, "-") {
			return true
		}
	}
	return false
}

// vdevTypeName turns "raidz2-3" into "raidz2", the name libzfs returns
// without VDEV_NAME_TYPE_ID.
func vdevTypeName(name string) string {
	if i := strings.LastIndex(name, "-"); i > 0 {
		return name[:i]
	}
	return name
}

// annotateZFSExec adds each pool member's use string and vdev state to the
// matching device, and records members that match no device in
// inv.Unmapped. A host without zpool on its PATH has no pools and is not an
// error.
func (c *Collector) annotateZFSExec(inv *drivelist.Inventory) error {
	statusP, err := c.run("zpool", "status", "-P")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			slog.Debug("zpool not available; no ZFS pool membership reported", "err", err)
			return nil
		}
		return fmt.Errorf("zpool status -P: %w", err)
	}
	statusG, err := c.run("zpool", "status", "-g")
	if err != nil {
		return fmt.Errorf("zpool status -g: %w", err)
	}
	guidOut, err := c.run("zpool", "get", "-Hp", "guid")
	if err != nil {
		return fmt.Errorf("zpool get guid: %w", err)
	}

	pools, err := parseZpoolStatus(string(statusP))
	if err != nil {
		return err
	}
	guidPools, err := parseZpoolStatus(string(statusG))
	if err != nil {
		return err
	}
	if len(pools) != len(guidPools) {
		return fmt.Errorf("zpool status: %d pools with -P but %d with -g", len(pools), len(guidPools))
	}
	poolGUIDs := parseZpoolGUIDs(string(guidOut))

	for i, pool := range pools {
		guid, ok := poolGUIDs[pool.name]
		if !ok {
			return fmt.Errorf("zpool get guid: no guid for pool %s", pool.name)
		}
		members, err := poolMembers(pool, guidPools[i], guid)
		if err != nil {
			return err
		}
		slices.SortFunc(members, func(a, b zpoolMember) int { return strings.Compare(a.path, b.path) })
		for _, m := range members {
			dev := inv.ByName(m.path)
			if dev == nil {
				inv.Unmapped = append(inv.Unmapped, drivelist.PoolMember{Pool: pool.name, Path: m.path, GUID: m.guid, State: m.state})
				continue
			}
			dev.Uses = append(dev.Uses, m.use)
			dev.MemberState = m.state
		}
	}
	return nil
}
