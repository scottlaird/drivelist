package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
)

// SmartSummary mirrors collect.SmartSummary without importing it.
type SmartSummary struct {
	Protocol      string
	Healthy       *bool
	PowerOnHours  *uint64
	TempC         *int
	Reallocated   *uint64
	Pending       *uint64
	Uncorrectable *uint64
	CRCErrors     *uint64
	ReadBytes     *uint64
	WriteBytes    *uint64
	PercentUsed   *uint32
	SelftestLast  string
}

// SmartSample is one smartctl run.
type SmartSample struct {
	Identity DriveIdentity
	Hostname string // set on read
	DevName  string
	TS       time.Time
	Summary  *SmartSummary // nil when Skipped
	RawGz    []byte        // optional on ingest
	Skipped  string
	HasRaw   bool // set on read
}

// RawKeep is how many raw smartctl documents are kept per drive, besides
// the first ever.
const RawKeep = 30

// IngestSmart stores samples. Counters that got worse since the previous
// sample (or a health verdict that turned false) raise one smart_warning
// event per drive per UTC day. Raw documents are kept: the oldest and the
// newest RawKeep. Samples for unknown drives are skipped.
func (s *Store) IngestSmart(ctx context.Context, host HostIdentity, samples []SmartSample) (int, error) {
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
		if len(ids) == 0 {
			continue
		}
		driveID := ids[0]
		if k.TS.IsZero() {
			k.TS = now
		}
		var rawID sql.NullInt64
		if len(k.RawGz) > 0 {
			res, err := t.ExecContext(ctx, `INSERT INTO smart_raw (drive_id, ts, json_gz) VALUES (?, ?, ?)`, driveID, k.TS.Unix(), k.RawGz)
			if err != nil {
				return 0, err
			}
			id, _ := res.LastInsertId()
			rawID = sql.NullInt64{Int64: id, Valid: true}
			if err := t.pruneRaw(driveID); err != nil {
				return 0, err
			}
		}
		prev, err := t.lastSummary(driveID)
		if err != nil {
			return 0, err
		}
		sm := k.Summary
		if sm == nil {
			sm = &SmartSummary{}
		}
		if _, err := t.ExecContext(ctx, `
			INSERT OR REPLACE INTO smart_sample (drive_id, host_id, ts, dev_name, protocol, healthy, power_on_hours, temp_c, reallocated, pending, uncorrectable, crc_errors, read_bytes, write_bytes, percent_used, selftest_last, skipped, raw_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			driveID, h.id, k.TS.Unix(), k.DevName, sm.Protocol, nullBool(sm.Healthy), nullU64(sm.PowerOnHours), nullInt(sm.TempC), nullU64(sm.Reallocated), nullU64(sm.Pending),
			nullU64(sm.Uncorrectable), nullU64(sm.CRCErrors), nullU64(sm.ReadBytes), nullU64(sm.WriteBytes), nullU32(sm.PercentUsed), sm.SelftestLast, k.Skipped, rawID); err != nil {
			return 0, err
		}
		stored++
		if k.Summary != nil {
			if err := t.smartWarnings(driveID, h.id, k, prev); err != nil {
				return 0, err
			}
		}
	}
	return stored, sqlTx.Commit()
}

// smartWarnings compares a sample with the previous one and records what
// got worse, once per drive per day.
func (t *tx) smartWarnings(driveID, hostID int64, k SmartSample, prev *SmartSummary) error {
	var reasons []string
	s := k.Summary
	if s.Healthy != nil && !*s.Healthy && (prev == nil || prev.Healthy == nil || *prev.Healthy) {
		reasons = append(reasons, "health assessment failed")
	}
	worse := func(name string, cur, old *uint64) {
		if cur == nil || *cur == 0 {
			return
		}
		if old == nil || *cur > *old {
			reasons = append(reasons, fmt.Sprintf("%s %d", name, *cur))
		}
	}
	var pr, pp, pu *uint64
	if prev != nil {
		pr, pp, pu = prev.Reallocated, prev.Pending, prev.Uncorrectable
	}
	worse("reallocated", s.Reallocated, pr)
	worse("pending", s.Pending, pp)
	worse("uncorrectable", s.Uncorrectable, pu)
	if len(reasons) == 0 {
		return nil
	}
	day := k.TS.UTC().Truncate(24 * time.Hour)
	var n int
	if err := t.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM event WHERE kind = ? AND drive_id = ? AND ts >= ? AND ts < ?`,
		EventSmartWarning, driveID, day.Unix(), day.Add(24*time.Hour).Unix()).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	saved := t.obs
	t.obs = k.TS.Unix()
	defer func() { t.obs = saved }()
	detail := map[string]any{"reasons": reasons, "dev_name": k.DevName, "protocol": s.Protocol}
	if s.Healthy != nil {
		detail["healthy"] = *s.Healthy
	}
	return t.event(EventSmartWarning, driveID, hostID, detail, "smart", 0)
}

// lastSummary returns the most recent non-skipped summary for a drive.
func (t *tx) lastSummary(driveID int64) (*SmartSummary, error) {
	rows, err := t.QueryContext(t.ctx, `SELECT `+smartColumns+` FROM smart_sample s WHERE s.drive_id = ? AND s.skipped = '' ORDER BY s.ts DESC LIMIT 1`, driveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	k, err := scanSmart(rows)
	if err != nil {
		return nil, err
	}
	return k.Summary, nil
}

// pruneRaw keeps the oldest raw document and the newest RawKeep.
func (t *tx) pruneRaw(driveID int64) error {
	_, err := t.ExecContext(t.ctx, `
		DELETE FROM smart_raw WHERE drive_id = ?1 AND raw_id NOT IN (
			SELECT raw_id FROM smart_raw WHERE drive_id = ?1 ORDER BY ts ASC LIMIT 1
		) AND raw_id NOT IN (
			SELECT raw_id FROM smart_raw WHERE drive_id = ?1 ORDER BY ts DESC LIMIT ?2
		)`, driveID, RawKeep)
	if err != nil {
		return err
	}
	// Samples whose raw document is gone lose the pointer.
	_, err = t.ExecContext(t.ctx, `UPDATE smart_sample SET raw_id = NULL WHERE drive_id = ?1 AND raw_id IS NOT NULL AND raw_id NOT IN (SELECT raw_id FROM smart_raw WHERE drive_id = ?1)`, driveID)
	return err
}

const smartColumns = `s.ts, s.protocol, s.healthy, s.power_on_hours, s.temp_c, s.reallocated, s.pending, s.uncorrectable, s.crc_errors, s.read_bytes, s.write_bytes, s.percent_used, s.selftest_last, s.skipped, s.raw_id IS NOT NULL`

func scanSmart(row interface{ Scan(...any) error }) (SmartSample, error) {
	var k SmartSample
	var ts int64
	var healthy sql.NullBool
	var poh, realloc, pending, uncorr, crc, rb, wb, pct, temp sql.NullInt64
	var proto, selftest string
	if err := row.Scan(&ts, &proto, &healthy, &poh, &temp, &realloc, &pending, &uncorr, &crc, &rb, &wb, &pct, &selftest, &k.Skipped, &k.HasRaw); err != nil {
		return k, err
	}
	k.TS = time.Unix(ts, 0).UTC()
	if k.Skipped == "" {
		sm := &SmartSummary{Protocol: proto, SelftestLast: selftest}
		if healthy.Valid {
			sm.Healthy = &healthy.Bool
		}
		sm.PowerOnHours = u64p(poh)
		if temp.Valid {
			v := int(temp.Int64)
			sm.TempC = &v
		}
		sm.Reallocated, sm.Pending, sm.Uncorrectable, sm.CRCErrors = u64p(realloc), u64p(pending), u64p(uncorr), u64p(crc)
		sm.ReadBytes, sm.WriteBytes = u64p(rb), u64p(wb)
		if pct.Valid {
			v := uint32(pct.Int64)
			sm.PercentUsed = &v
		}
		k.Summary = sm
	}
	return k, nil
}

// SmartSamples returns a drive's samples since a time, newest first, and
// (when withRaw) the newest stored raw document, uncompressed.
func (s *Store) SmartSamples(ctx context.Context, ref string, since time.Time, withRaw bool) (Drive, []SmartSample, []byte, time.Time, error) {
	id, err := s.ResolveDrive(ctx, ref)
	if err != nil {
		return Drive{}, nil, nil, time.Time{}, err
	}
	ds, err := s.drives(ctx, `WHERE d.drive_id = ?`, id)
	if err != nil || len(ds) == 0 {
		return Drive{}, nil, nil, time.Time{}, fmt.Errorf("drive %d: %w", id, ErrNotFound)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT h.hostname, s.dev_name, `+smartColumns+` FROM smart_sample s JOIN host h USING (host_id) WHERE s.drive_id = ? AND s.ts >= ? ORDER BY s.ts DESC`, id, since.Unix())
	if err != nil {
		return Drive{}, nil, nil, time.Time{}, err
	}
	defer rows.Close()
	var out []SmartSample
	for rows.Next() {
		var hostname, dev string
		k, err := scanSmart(prefixScanner{rows, &hostname, &dev})
		if err != nil {
			return Drive{}, nil, nil, time.Time{}, err
		}
		k.Hostname, k.DevName = hostname, dev
		k.Identity = DriveIdentity{WWN: ds[0].WWN, Model: ds[0].Model, Serial: ds[0].Serial}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return Drive{}, nil, nil, time.Time{}, err
	}
	var raw []byte
	var rawTS time.Time
	if withRaw {
		var gz []byte
		var ts int64
		err := s.db.QueryRowContext(ctx, `SELECT ts, json_gz FROM smart_raw WHERE drive_id = ? ORDER BY ts DESC LIMIT 1`, id).Scan(&ts, &gz)
		if err != nil && err != sql.ErrNoRows {
			return Drive{}, nil, nil, time.Time{}, err
		}
		if err == nil {
			raw, err = gunzip(gz)
			if err != nil {
				return Drive{}, nil, nil, time.Time{}, err
			}
			rawTS = time.Unix(ts, 0).UTC()
		}
	}
	return ds[0], out, raw, rawTS, nil
}

// prefixScanner scans two leading columns into the given targets, then
// the rest through the wrapped row.
type prefixScanner struct {
	rows *sql.Rows
	a, b *string
}

func (p prefixScanner) Scan(dest ...any) error {
	return p.rows.Scan(append([]any{p.a, p.b}, dest...)...)
}

func gunzip(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func nullBool(p *bool) sql.NullBool {
	if p == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Bool: *p, Valid: true}
}

func nullU64(p *uint64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}

func nullU32(p *uint32) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}

func nullInt(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}

func u64p(n sql.NullInt64) *uint64 {
	if !n.Valid {
		return nil
	}
	v := uint64(n.Int64)
	return &v
}

// SmartRow is one placed drive with its newest SMART reading.
type SmartRow struct {
	Drive       Drive
	Sample      *SmartSample // the newest sample with a summary; nil when none
	LastSkipped string       // why the newest sample of all was skipped, when it is newer than Sample
}

// Problem reports whether the reading says something is wrong: health
// failed, or any of the error counters is nonzero, or the drive is at 90%
// of its rated life. A drive without a reading is not a problem, just
// unknown.
func (r SmartRow) Problem() bool {
	if r.Sample == nil || r.Sample.Summary == nil {
		return false
	}
	m := r.Sample.Summary
	nz := func(p *uint64) bool { return p != nil && *p > 0 }
	return (m.Healthy != nil && !*m.Healthy) || nz(m.Reallocated) || nz(m.Pending) || nz(m.Uncorrectable) || nz(m.CRCErrors) ||
		(m.PercentUsed != nil && *m.PercentUsed >= 90)
}

// ListSmart returns every placed drive (on one host, or all) with its
// newest SMART reading, problems first, then by host and slot. With
// problems set, only drives whose reading says something is wrong.
func (s *Store) ListSmart(ctx context.Context, host string, problems bool) ([]SmartRow, error) {
	drives, err := s.ListDrives(ctx, DriveFilter{Host: host})
	if err != nil {
		return nil, err
	}
	var out []SmartRow
	for _, d := range drives {
		if d.Current == nil {
			continue
		}
		row := SmartRow{Drive: d}
		latest, err := s.db.QueryContext(ctx, `SELECT h.hostname, s.dev_name, `+smartColumns+` FROM smart_sample s JOIN host h USING (host_id) WHERE s.drive_id = ? AND s.skipped = '' ORDER BY s.ts DESC LIMIT 1`, d.ID)
		if err != nil {
			return nil, err
		}
		if latest.Next() {
			var hostname, dev string
			k, err := scanSmart(prefixScanner{latest, &hostname, &dev})
			if err != nil {
				latest.Close()
				return nil, err
			}
			k.Hostname, k.DevName = hostname, dev
			k.Identity = DriveIdentity{WWN: d.WWN, Model: d.Model, Serial: d.Serial}
			row.Sample = &k
		}
		if err := latest.Close(); err != nil {
			return nil, err
		}
		var skipped string
		var ts int64
		err = s.db.QueryRowContext(ctx, `SELECT skipped, ts FROM smart_sample WHERE drive_id = ? ORDER BY ts DESC LIMIT 1`, d.ID).Scan(&skipped, &ts)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil && skipped != "" && (row.Sample == nil || time.Unix(ts, 0).After(row.Sample.TS)) {
			row.LastSkipped = skipped
		}
		if problems && !row.Problem() {
			continue
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := out[i].Problem(), out[j].Problem()
		if pi != pj {
			return pi
		}
		return false // ListDrives already ordered by host and slot
	})
	return out, nil
}
