package store

import (
	"context"
	"fmt"
	"time"
)

// KernelSample is one hour of one class of kernel log line about one drive.
type KernelSample struct {
	Identity    DriveIdentity
	Hostname    string // set on read
	DevName     string
	BucketStart time.Time
	BucketSecs  int
	Class       string
	Code        string
	Count       int
	Sample      string
}

// KernelWarningClasses get a kernel_warning event the first time they are
// seen for a drive on a given day.
var KernelWarningClasses = map[string]bool{
	"predictive_failure": true, "medium_error": true, "hardware_error": true, "io_error": true, "timeout": true,
}

// IngestKernel stores hourly kernel log counts. A bucket already stored is
// replaced, so an agent retry after a lost acknowledgement is harmless.
// Samples for drives the server does not know are skipped. It returns how
// many were stored.
func (s *Store) IngestKernel(ctx context.Context, host HostIdentity, samples []KernelSample) (int, error) {
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
		if HostKernelClasses[k.Class] {
			if err := t.hardwareError(h.id, k); err != nil {
				return 0, err
			}
			stored++
			continue
		}
		ids, err := t.drivesForKeys(k.Identity.Keys())
		if err != nil {
			return 0, err
		}
		if len(ids) == 0 {
			continue
		}
		driveID := ids[0]
		if _, err := t.ExecContext(ctx, `
			INSERT INTO kmsg_sample (drive_id, host_id, bucket_start, bucket_secs, class, scsi_code, count, sample)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (drive_id, bucket_start, class, scsi_code) DO UPDATE SET
				host_id = excluded.host_id, bucket_secs = excluded.bucket_secs, count = excluded.count, sample = excluded.sample`,
			driveID, h.id, k.BucketStart.Unix(), k.BucketSecs, k.Class, k.Code, k.Count, k.Sample); err != nil {
			return 0, err
		}
		stored++
		if KernelWarningClasses[k.Class] {
			if err := t.kernelWarning(driveID, h.id, k); err != nil {
				return 0, err
			}
		}
	}
	return stored, sqlTx.Commit()
}

// HostKernelClasses are about the host itself, not a drive: memory and
// machine-check errors. Each becomes a hardware_error event, once per
// class, location and UTC day; there is no drive to keep samples under.
var HostKernelClasses = map[string]bool{"hw_corrected": true, "hw_uncorrected": true}

// hardwareError records one hardware_error event per host, class,
// location and UTC day, timestamped at the bucket.
func (t *tx) hardwareError(hostID int64, k KernelSample) error {
	day := k.BucketStart.UTC().Truncate(24 * time.Hour)
	var n int
	if err := t.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM event WHERE kind = ? AND host_id = ? AND ts >= ? AND ts < ? AND detail LIKE ?`,
		EventHardwareError, hostID, day.Unix(), day.Add(24*time.Hour).Unix(), fmt.Sprintf(`%%"class":"%s"%%"code":"%s"%%`, k.Class, k.Code)).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	saved := t.obs
	t.obs = k.BucketStart.Unix()
	defer func() { t.obs = saved }()
	return t.event(EventHardwareError, 0, hostID, map[string]any{"class": k.Class, "code": k.Code, "count": k.Count, "sample": k.Sample}, "kernel", 0)
}

// kernelWarning records one kernel_warning event per drive, class and UTC
// day, timestamped at the bucket.
func (t *tx) kernelWarning(driveID, hostID int64, k KernelSample) error {
	day := k.BucketStart.UTC().Truncate(24 * time.Hour)
	var n int
	if err := t.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM event WHERE kind = ? AND drive_id = ? AND ts >= ? AND ts < ? AND detail LIKE ?`,
		EventKernelWarning, driveID, day.Unix(), day.Add(24*time.Hour).Unix(), fmt.Sprintf(`%%"class":"%s"%%`, k.Class)).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	saved := t.obs
	t.obs = k.BucketStart.Unix()
	defer func() { t.obs = saved }()
	return t.event(EventKernelWarning, driveID, hostID, map[string]any{"class": k.Class, "code": k.Code, "count": k.Count, "dev_name": k.DevName, "sample": k.Sample}, "kernel", 0)
}

// KernelSamples returns a drive's buckets since a time, newest first.
func (s *Store) KernelSamples(ctx context.Context, ref string, since time.Time) (Drive, []KernelSample, error) {
	id, err := s.ResolveDrive(ctx, ref)
	if err != nil {
		return Drive{}, nil, err
	}
	ds, err := s.drives(ctx, `WHERE d.drive_id = ?`, id)
	if err != nil || len(ds) == 0 {
		return Drive{}, nil, fmt.Errorf("drive %d: %w", id, ErrNotFound)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.hostname, k.bucket_start, k.bucket_secs, k.class, k.scsi_code, k.count, k.sample
		FROM kmsg_sample k JOIN host h USING (host_id)
		WHERE k.drive_id = ? AND k.bucket_start >= ?
		ORDER BY k.bucket_start DESC, k.class, k.scsi_code`, id, since.Unix())
	if err != nil {
		return Drive{}, nil, err
	}
	defer rows.Close()
	var out []KernelSample
	for rows.Next() {
		k := KernelSample{Identity: DriveIdentity{WWN: ds[0].WWN, Model: ds[0].Model, Serial: ds[0].Serial}}
		var start int64
		if err := rows.Scan(&k.Hostname, &start, &k.BucketSecs, &k.Class, &k.Code, &k.Count, &k.Sample); err != nil {
			return Drive{}, nil, err
		}
		k.BucketStart = time.Unix(start, 0).UTC()
		out = append(out, k)
	}
	return ds[0], out, rows.Err()
}
