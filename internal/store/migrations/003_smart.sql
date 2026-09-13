-- SMART summaries per sample. Columns a bus does not report stay NULL.
CREATE TABLE smart_sample (
  drive_id        INTEGER NOT NULL REFERENCES drive(drive_id),
  host_id         INTEGER NOT NULL REFERENCES host(host_id),
  ts              INTEGER NOT NULL,
  dev_name        TEXT NOT NULL DEFAULT '',
  protocol        TEXT NOT NULL DEFAULT '',
  healthy         INTEGER,
  power_on_hours  INTEGER,
  temp_c          INTEGER,
  reallocated     INTEGER,
  pending         INTEGER,
  uncorrectable   INTEGER,
  crc_errors      INTEGER,
  read_bytes      INTEGER,
  write_bytes     INTEGER,
  percent_used    INTEGER,
  selftest_last   TEXT NOT NULL DEFAULT '',
  skipped         TEXT NOT NULL DEFAULT '',   -- 'standby' | 'timeout' | 'unsupported' | 'error: …'; other columns NULL
  raw_id          INTEGER,
  PRIMARY KEY (drive_id, ts)
);

-- Full smartctl -j output, gzip'd. The first ever and the last 30 per drive
-- are kept.
CREATE TABLE smart_raw (
  raw_id          INTEGER PRIMARY KEY,
  drive_id        INTEGER NOT NULL REFERENCES drive(drive_id),
  ts              INTEGER NOT NULL,
  json_gz         BLOB NOT NULL
);
CREATE INDEX smart_raw_drive ON smart_raw(drive_id, ts);
