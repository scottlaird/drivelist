package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// IOSample is one hour of one drive's I/O.
type IOSample struct {
	Identity    DriveIdentity
	Hostname    string // set on read
	DevName     string
	BucketStart time.Time
	BucketSecs  int
	Reads       uint64
	Writes      uint64
	ReadBytes   uint64
	WriteBytes  uint64
	ReadMs      uint64
	WriteMs     uint64
	IOMs        uint64
	WeightedMs  uint64
	RAwaitMax   float64
	WAwaitMax   float64
	UtilMax     float64
	AwaitReads  uint64 // completions ReadMs covers; Reads unless intervals were rejected
	AwaitWrites uint64
	Glitches    int // time counters the agent rejected
}

// RAwait, WAwait and Util derive the iostat rates from the bucket.
func (s IOSample) RAwait() float64 { return div(s.ReadMs, s.AwaitReads) }
func (s IOSample) WAwait() float64 { return div(s.WriteMs, s.AwaitWrites) }
func (s IOSample) Util() float64 {
	if s.BucketSecs == 0 {
		return 0
	}
	return min(float64(s.IOMs)/float64(s.BucketSecs*1000), 1)
}

func div(a, b uint64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// IngestIO stores buckets, replacing any already stored for the same drive
// and hour. Samples for unknown drives are skipped.
func (s *Store) IngestIO(ctx context.Context, host HostIdentity, samples []IOSample) (int, error) {
	now := s.now()
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer sqlTx.Rollback()
	t := &tx{Tx: sqlTx, ctx: ctx, now: now.Unix(), obs: now.Unix()}
	h, err := t.upsertHost(host)
	if err != nil {
		return 0, err
	}
	stored := 0
	for _, k := range samples {
		ids, err := t.drivesForKeys(k.Identity.Keys())
		if err != nil {
			return 0, err
		}
		if len(ids) == 0 || k.BucketSecs <= 0 {
			continue
		}
		// An agent from before glitch rejection covers every completion.
		if k.AwaitReads == 0 && k.AwaitWrites == 0 && k.Glitches == 0 {
			k.AwaitReads, k.AwaitWrites = k.Reads, k.Writes
		}
		if err := t.insertIOSample(ids[0], h.id, k); err != nil {
			return 0, err
		}
		stored++
	}
	return stored, sqlTx.Commit()
}

// insertIOSample stores one bucket, replacing any for the same drive and
// start.
func (t *tx) insertIOSample(driveID, hostID int64, k IOSample) error {
	_, err := t.ExecContext(t.ctx, `
		INSERT OR REPLACE INTO io_sample (drive_id, host_id, dev_name, bucket_start, bucket_secs, reads, writes, read_bytes, write_bytes, read_ms, write_ms, io_ms, weighted_ms, r_await_max, w_await_max, util_max, await_reads, await_writes, glitches)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		driveID, hostID, k.DevName, k.BucketStart.Unix(), k.BucketSecs, k.Reads, k.Writes, k.ReadBytes, k.WriteBytes, k.ReadMs, k.WriteMs, k.IOMs, k.WeightedMs, k.RAwaitMax, k.WAwaitMax, k.UtilMax, k.AwaitReads, k.AwaitWrites, k.Glitches)
	return err
}

const ioColumns = `h.hostname, i.dev_name, i.bucket_start, i.bucket_secs, i.reads, i.writes, i.read_bytes, i.write_bytes, i.read_ms, i.write_ms, i.io_ms, i.weighted_ms, i.r_await_max, i.w_await_max, i.util_max, i.await_reads, i.await_writes, i.glitches`

// IOSamples returns a drive's hourly buckets since a time, newest first.
// Buckets rolled into io_daily are returned as day-sized samples.
func (s *Store) IOSamples(ctx context.Context, ref string, since time.Time) (Drive, []IOSample, error) {
	id, err := s.ResolveDrive(ctx, ref)
	if err != nil {
		return Drive{}, nil, err
	}
	ds, err := s.drives(ctx, `WHERE d.drive_id = ?`, id)
	if err != nil || len(ds) == 0 {
		return Drive{}, nil, fmt.Errorf("drive %d: %w", id, ErrNotFound)
	}
	identity := DriveIdentity{WWN: ds[0].WWN, Model: ds[0].Model, Serial: ds[0].Serial}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+ioColumns+` FROM io_sample i JOIN host h USING (host_id) WHERE i.drive_id = ? AND i.bucket_start >= ?
		UNION ALL
		SELECT h.hostname, '', i.day, i.secs, i.reads, i.writes, i.read_bytes, i.write_bytes, i.read_ms, i.write_ms, i.io_ms, i.weighted_ms, i.r_await_max, i.w_await_max, i.util_max, i.await_reads, i.await_writes, i.glitches
		FROM io_daily i JOIN host h USING (host_id) WHERE i.drive_id = ? AND i.day >= ?
		ORDER BY 3 DESC`, id, since.Unix(), id, since.Unix())
	if err != nil {
		return Drive{}, nil, err
	}
	defer rows.Close()
	var out []IOSample
	for rows.Next() {
		k := IOSample{Identity: identity}
		var start int64
		if err := rows.Scan(&k.Hostname, &k.DevName, &start, &k.BucketSecs, &k.Reads, &k.Writes, &k.ReadBytes, &k.WriteBytes, &k.ReadMs, &k.WriteMs, &k.IOMs, &k.WeightedMs, &k.RAwaitMax, &k.WAwaitMax, &k.UtilMax, &k.AwaitReads, &k.AwaitWrites, &k.Glitches); err != nil {
			return Drive{}, nil, err
		}
		k.BucketStart = time.Unix(start, 0).UTC()
		out = append(out, k)
	}
	return ds[0], out, rows.Err()
}

// IOComparison is one placed drive's averages over a window beside its
// vdev's median.
type IOComparison struct {
	Group       string // the vdev, from the use string with the leaf removed; "" for no pool
	Hostname    string
	Serial      string
	Model       string
	DevName     string
	Reads       uint64
	Writes      uint64
	RAwait      float64
	WAwait      float64
	Util        float64
	GroupRAwait float64
	GroupWAwait float64
	GroupUtil   float64
	GroupSize   int
	Glitches    int // time counters rejected in the window
}

// CompareIO aggregates io_sample over the window for every drive with an
// open placement (on one host, or all), groups drives by vdev, and
// attaches each group's medians. Rows come back by group, worst read
// latency ratio first, so the outliers lead.
func (s *Store) CompareIO(ctx context.Context, host string, since time.Time) ([]IOComparison, error) {
	where := "p.ended_at IS NULL"
	var args []any
	if host != "" {
		h, err := s.ResolveHost(ctx, host)
		if err != nil {
			return nil, err
		}
		where += " AND p.host_id = ?"
		args = append(args, h.ID)
	}
	args = append(args, since.Unix())
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.hostname, d.serial, d.model, p.dev_name, p.uses,
		       COALESCE(SUM(i.reads), 0), COALESCE(SUM(i.writes), 0), COALESCE(SUM(i.read_ms), 0), COALESCE(SUM(i.write_ms), 0),
		       COALESCE(SUM(i.io_ms), 0), COALESCE(SUM(i.bucket_secs), 0), COALESCE(SUM(i.await_reads), 0), COALESCE(SUM(i.await_writes), 0), COALESCE(SUM(i.glitches), 0)
		FROM placement p JOIN drive d USING (drive_id) JOIN host h USING (host_id)
		LEFT JOIN io_sample i ON i.drive_id = p.drive_id AND i.bucket_start >= ?
		WHERE `+where+`
		GROUP BY p.placement_id ORDER BY h.hostname, d.serial`, append([]any{args[len(args)-1]}, args[:len(args)-1]...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IOComparison
	for rows.Next() {
		var c IOComparison
		var uses string
		var readMs, writeMs, ioMs, secs, awaitReads, awaitWrites uint64
		if err := rows.Scan(&c.Hostname, &c.Serial, &c.Model, &c.DevName, &uses, &c.Reads, &c.Writes, &readMs, &writeMs, &ioMs, &secs, &awaitReads, &awaitWrites, &c.Glitches); err != nil {
			return nil, err
		}
		if secs == 0 {
			continue // no samples in the window
		}
		c.Group = vdevGroup(uses)
		c.RAwait, c.WAwait = div(readMs, awaitReads), div(writeMs, awaitWrites)
		c.Util = min(float64(ioMs)/float64(secs*1000), 1)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Group medians.
	byGroup := map[string][]int{}
	for i, c := range out {
		byGroup[c.Group] = append(byGroup[c.Group], i)
	}
	for _, idx := range byGroup {
		var r, w, u []float64
		for _, i := range idx {
			r, w, u = append(r, out[i].RAwait), append(w, out[i].WAwait), append(u, out[i].Util)
		}
		mr, mw, mu := median(r), median(w), median(u)
		for _, i := range idx {
			out[i].GroupRAwait, out[i].GroupWAwait, out[i].GroupUtil, out[i].GroupSize = mr, mw, mu, len(idx)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return ratioTo(out[i].RAwait, out[i].GroupRAwait) > ratioTo(out[j].RAwait, out[j].GroupRAwait)
	})
	return out, nil
}

// vdevGroup derives the grouping key from a placement's uses: the first
// zfs use with its "> disk <guid>" leaf removed.
func vdevGroup(usesJSON string) string {
	var uses []string
	json.Unmarshal([]byte(usesJSON), &uses)
	for _, u := range uses {
		if !strings.HasPrefix(u, "zfs > ") {
			continue
		}
		if i := strings.LastIndex(u, " > disk "); i > 0 {
			return u[:i]
		}
		return u
	}
	return ""
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

func ratioTo(v, base float64) float64 {
	if base == 0 {
		if v == 0 {
			return 1
		}
		return 1e9
	}
	return v / base
}

// RetainIO rolls hourly buckets older than keep into io_daily and deletes
// them. Run it daily; it is idempotent.
func (s *Store) RetainIO(ctx context.Context, keep time.Duration) (int64, error) {
	cutoff := s.now().Add(-keep).UTC().Truncate(24 * time.Hour).Unix()
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer sqlTx.Rollback()
	if _, err := sqlTx.ExecContext(ctx, `
		INSERT INTO io_daily (drive_id, host_id, day, secs, reads, writes, read_bytes, write_bytes, read_ms, write_ms, io_ms, weighted_ms, r_await_max, w_await_max, util_max, await_reads, await_writes, glitches)
		SELECT drive_id, host_id, (bucket_start / 86400) * 86400, SUM(bucket_secs), SUM(reads), SUM(writes), SUM(read_bytes), SUM(write_bytes),
		       SUM(read_ms), SUM(write_ms), SUM(io_ms), SUM(weighted_ms), MAX(r_await_max), MAX(w_await_max), MAX(util_max), SUM(await_reads), SUM(await_writes), SUM(glitches)
		FROM io_sample WHERE bucket_start < ?
		GROUP BY drive_id, host_id, (bucket_start / 86400)
		ON CONFLICT (drive_id, day) DO UPDATE SET
		  secs = secs + excluded.secs, reads = reads + excluded.reads, writes = writes + excluded.writes,
		  read_bytes = read_bytes + excluded.read_bytes, write_bytes = write_bytes + excluded.write_bytes,
		  read_ms = read_ms + excluded.read_ms, write_ms = write_ms + excluded.write_ms,
		  io_ms = io_ms + excluded.io_ms, weighted_ms = weighted_ms + excluded.weighted_ms,
		  r_await_max = MAX(r_await_max, excluded.r_await_max), w_await_max = MAX(w_await_max, excluded.w_await_max), util_max = MAX(util_max, excluded.util_max),
		  await_reads = await_reads + excluded.await_reads, await_writes = await_writes + excluded.await_writes, glitches = glitches + excluded.glitches`, cutoff); err != nil {
		return 0, err
	}
	res, err := sqlTx.ExecContext(ctx, `DELETE FROM io_sample WHERE bucket_start < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, sqlTx.Commit()
}
