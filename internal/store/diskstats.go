package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/scottlaird/drivelist/collect"
)

// maxPullInterval is the longest gap between two pulls that still makes
// one bucket. Beyond it the counters are only remembered, so a host that
// was not pulled for a week does not get a week-long bucket that every
// per-hour comparison would misread.
const maxPullInterval = 48 * time.Hour

// IngestDiskStats takes raw /proc/diskstats counters read at `at` on a
// host with no agent, diffs each device against the counters the last
// pull left, and stores the interval as one io_sample bucket for the
// drive currently placed under that device name, with the same glitch
// rejection the agent applies. The first pull, a pull from a new boot,
// and counters that went backwards leave no bucket, only the new
// counters. It returns how many buckets were stored.
func (s *Store) IngestDiskStats(ctx context.Context, host HostIdentity, at time.Time, uptime time.Duration, stats []collect.DiskStat) (int, error) {
	now := s.now()
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer sqlTx.Rollback()
	t := &tx{Tx: sqlTx, ctx: ctx, now: now.Unix(), obs: at.Unix()}
	h, err := t.upsertHost(host)
	if err != nil {
		return 0, err
	}
	// The drive behind each device name, from the host's open placements.
	drives := map[string]int64{}
	rows, err := t.QueryContext(ctx, `SELECT dev_name, drive_id FROM placement WHERE host_id = ? AND ended_at IS NULL`, h.id)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var dev string
		var id int64
		if err := rows.Scan(&dev, &id); err != nil {
			rows.Close()
			return 0, err
		}
		drives[dev] = id
	}
	rows.Close()

	stored := 0
	for _, cur := range stats {
		driveID, known := drives[cur.Name]
		if !known {
			continue
		}
		var prev collect.DiskStat
		var prevTs int64
		var prevUptime float64
		var prevBoot string
		err := t.QueryRowContext(ctx, `SELECT ts, uptime_secs, boot_id, reads, writes, sectors_read, sectors_write, read_ms, write_ms, io_ms, weighted_ms FROM io_counter WHERE host_id = ? AND dev_name = ?`, h.id, cur.Name).
			Scan(&prevTs, &prevUptime, &prevBoot, &prev.Reads, &prev.Writes, &prev.SectorsRead, &prev.SectorsWrite, &prev.ReadMs, &prev.WriteMs, &prev.IOMs, &prev.WeightedIOMs)
		switch {
		case err == sql.ErrNoRows:
		case err != nil:
			return 0, err
		default:
			interval := at.Sub(time.Unix(prevTs, 0))
			sameBoot := host.BootID == "" || prevBoot == "" || host.BootID == prevBoot
			if sameBoot && interval > 0 && interval <= maxPullInterval {
				if d, ok := collect.Delta(prev, cur, interval); ok {
					d.DropGlitches(time.Duration(prevUptime * float64(time.Second)))
					k := IOSample{DevName: cur.Name, BucketStart: time.Unix(prevTs, 0), BucketSecs: int(interval.Seconds()),
						Reads: d.Reads, Writes: d.Writes, ReadBytes: d.ReadBytes, WriteBytes: d.WriteBytes,
						ReadMs: d.ReadMs, WriteMs: d.WriteMs, IOMs: d.IOMs, WeightedMs: d.WeightedMs,
						AwaitReads: d.AwaitReads, AwaitWrites: d.AwaitWrites, Glitches: d.Glitches}
					// One interval, so the worst sub-sample is the interval.
					k.RAwaitMax, k.WAwaitMax, k.UtilMax = k.RAwait(), k.WAwait(), k.Util()
					if err := t.insertIOSample(driveID, h.id, k); err != nil {
						return 0, err
					}
					stored++
				}
			}
		}
		if _, err := t.ExecContext(ctx, `
			INSERT OR REPLACE INTO io_counter (host_id, dev_name, ts, uptime_secs, boot_id, reads, writes, sectors_read, sectors_write, read_ms, write_ms, io_ms, weighted_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			h.id, cur.Name, at.Unix(), uptime.Seconds(), host.BootID, cur.Reads, cur.Writes, cur.SectorsRead, cur.SectorsWrite, cur.ReadMs, cur.WriteMs, cur.IOMs, cur.WeightedIOMs); err != nil {
			return 0, err
		}
	}
	return stored, sqlTx.Commit()
}
