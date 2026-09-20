-- The last /proc/diskstats counters seen from a host that is pulled
-- rather than running the agent, per device, so the next pull can be
-- diffed against them into one io_sample bucket covering the interval.
-- The agent does this diffing itself, every minute; a pulled host has
-- nobody to do it on its side.
CREATE TABLE io_counter (
  host_id        INTEGER NOT NULL REFERENCES host(host_id),
  dev_name       TEXT NOT NULL,
  ts             INTEGER NOT NULL,          -- when the counters were read
  uptime_secs    REAL NOT NULL DEFAULT 0,
  boot_id        TEXT NOT NULL DEFAULT '',
  reads          INTEGER NOT NULL,
  writes         INTEGER NOT NULL,
  sectors_read   INTEGER NOT NULL,
  sectors_write  INTEGER NOT NULL,
  read_ms        INTEGER NOT NULL,
  write_ms       INTEGER NOT NULL,
  io_ms          INTEGER NOT NULL,
  weighted_ms    INTEGER NOT NULL,
  PRIMARY KEY (host_id, dev_name)
);
