-- Kernel log lines about a drive, classified and counted per hour by the
-- agent. Raw noise never leaves the host; one sample line per bucket
-- survives for forensics.
CREATE TABLE kmsg_sample (
  drive_id       INTEGER NOT NULL REFERENCES drive(drive_id),
  host_id        INTEGER NOT NULL REFERENCES host(host_id),
  bucket_start   INTEGER NOT NULL,
  bucket_secs    INTEGER NOT NULL,
  class          TEXT NOT NULL,
  scsi_code      TEXT NOT NULL DEFAULT '',   -- "key:asc:ascq" hex, e.g. "1:5d:90"; '' for non-SCSI
  count          INTEGER NOT NULL,
  sample         TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (drive_id, bucket_start, class, scsi_code)
);
CREATE INDEX kmsg_host_time ON kmsg_sample(host_id, bucket_start);
