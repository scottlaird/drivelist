-- Agent-side rollup of /proc/diskstats deltas per hour. Counters are
-- per-bucket deltas, so the server never handles wraparound or reboots.
CREATE TABLE io_sample (
  drive_id       INTEGER NOT NULL REFERENCES drive(drive_id),
  host_id        INTEGER NOT NULL REFERENCES host(host_id),
  dev_name       TEXT NOT NULL DEFAULT '',
  bucket_start   INTEGER NOT NULL,
  bucket_secs    INTEGER NOT NULL,
  reads          INTEGER NOT NULL,
  writes         INTEGER NOT NULL,
  read_bytes     INTEGER NOT NULL,
  write_bytes    INTEGER NOT NULL,
  read_ms        INTEGER NOT NULL,
  write_ms       INTEGER NOT NULL,
  io_ms          INTEGER NOT NULL,
  weighted_ms    INTEGER NOT NULL,
  r_await_max    REAL NOT NULL DEFAULT 0,
  w_await_max    REAL NOT NULL DEFAULT 0,
  util_max       REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (drive_id, bucket_start)
);
CREATE INDEX io_host_time ON io_sample(host_id, bucket_start);

-- Daily rollup, written by retention before it prunes hourly rows.
CREATE TABLE io_daily (
  drive_id       INTEGER NOT NULL REFERENCES drive(drive_id),
  host_id        INTEGER NOT NULL REFERENCES host(host_id),
  day            INTEGER NOT NULL,          -- unix time of 00:00 UTC
  secs           INTEGER NOT NULL,          -- seconds covered
  reads          INTEGER NOT NULL,
  writes         INTEGER NOT NULL,
  read_bytes     INTEGER NOT NULL,
  write_bytes    INTEGER NOT NULL,
  read_ms        INTEGER NOT NULL,
  write_ms       INTEGER NOT NULL,
  io_ms          INTEGER NOT NULL,
  weighted_ms    INTEGER NOT NULL,
  r_await_max    REAL NOT NULL DEFAULT 0,
  w_await_max    REAL NOT NULL DEFAULT 0,
  util_max       REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (drive_id, day)
);
