-- Hosts are keyed by machine id; the hostname is a label.
CREATE TABLE host (
  host_id        INTEGER PRIMARY KEY,
  machine_id     TEXT NOT NULL UNIQUE,
  hostname       TEXT NOT NULL,
  os             TEXT NOT NULL DEFAULT '',
  agent_version  TEXT NOT NULL DEFAULT '',
  first_seen     INTEGER NOT NULL,
  last_report    INTEGER,            -- server time of the last accepted report
  last_observed  INTEGER,            -- agent observed_at of the last applied report; reports older than this are rejected
  stale_since    INTEGER,            -- set by the sweeper, cleared by the next report
  degraded       INTEGER NOT NULL DEFAULT 0,  -- 1 while the latest report could not identify every device
  current_snapshot_id INTEGER        -- the snapshot heartbeats extend
);

CREATE TABLE drive (
  drive_id       INTEGER PRIMARY KEY,
  wwn            TEXT NOT NULL DEFAULT '',
  vendor         TEXT NOT NULL DEFAULT '',
  model          TEXT NOT NULL DEFAULT '',
  serial         TEXT NOT NULL DEFAULT '',
  size_bytes     INTEGER NOT NULL DEFAULT 0,
  bus            TEXT NOT NULL DEFAULT '',
  first_seen     INTEGER NOT NULL,
  last_seen      INTEGER NOT NULL,
  status         TEXT NOT NULL DEFAULT 'ok',  -- ok|suspect|bad|shelved|retired; mirror of the latest status_changed event
  merged_into    INTEGER REFERENCES drive(drive_id)
);

-- Every way a drive has ever been identified. Lookup is by key, never by column.
CREATE TABLE drive_key (
  key            TEXT PRIMARY KEY,    -- 'wwn:0x5000cca25206c808' | 'serial:HUH721008AL5204/7SG3RM2G'
  drive_id       INTEGER NOT NULL REFERENCES drive(drive_id)
);
CREATE INDEX drive_key_drive ON drive_key(drive_id);

-- One row per distinct host state. Heartbeats with an unchanged hash only
-- advance last_at.
CREATE TABLE snapshot (
  snapshot_id    INTEGER PRIMARY KEY,
  host_id        INTEGER NOT NULL REFERENCES host(host_id),
  first_at       INTEGER NOT NULL,    -- observed_at of the report that introduced this state
  last_at        INTEGER NOT NULL,    -- observed_at of the last heartbeat confirming it
  received_at    INTEGER NOT NULL,
  content_hash   TEXT NOT NULL,
  complete       INTEGER NOT NULL,    -- 0 if a device failed identification and could not be attributed by bay
  device_count   INTEGER NOT NULL,
  errors         TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX snapshot_host_time ON snapshot(host_id, first_at);

CREATE TABLE snapshot_device (
  snapshot_id    INTEGER NOT NULL REFERENCES snapshot(snapshot_id),
  dev_name       TEXT NOT NULL,
  drive_id       INTEGER REFERENCES drive(drive_id),   -- NULL when unidentified
  expander       TEXT NOT NULL DEFAULT '',
  bay            TEXT NOT NULL DEFAULT '',
  enclosure_path TEXT NOT NULL DEFAULT '',
  size_bytes     INTEGER NOT NULL DEFAULT 0,
  uses           TEXT NOT NULL,       -- JSON array, sorted
  member_state   TEXT NOT NULL DEFAULT '',
  dev_links      TEXT NOT NULL DEFAULT '[]',
  scsi_addr      TEXT NOT NULL DEFAULT '',
  error          TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (snapshot_id, dev_name)
);

-- Pool members the agent could not map to a present block device.
CREATE TABLE ghost (
  ghost_id       INTEGER PRIMARY KEY,
  host_id        INTEGER NOT NULL REFERENCES host(host_id),
  pool           TEXT NOT NULL,
  member_path    TEXT NOT NULL,
  member_guid    TEXT NOT NULL DEFAULT '',
  member_state   TEXT NOT NULL DEFAULT '',
  drive_id       INTEGER REFERENCES drive(drive_id),   -- resolved from the path when possible
  first_seen     INTEGER NOT NULL,
  last_seen      INTEGER NOT NULL,
  ended_at       INTEGER             -- NULL while the pool still references it
);
CREATE INDEX ghost_open ON ghost(host_id) WHERE ended_at IS NULL;

-- A continuous interval of one drive on one host in one slot with one set of
-- uses. These are the drive's history.
CREATE TABLE placement (
  placement_id   INTEGER PRIMARY KEY,
  drive_id       INTEGER NOT NULL REFERENCES drive(drive_id),
  host_id        INTEGER NOT NULL REFERENCES host(host_id),
  expander       TEXT NOT NULL DEFAULT '',
  bay            TEXT NOT NULL DEFAULT '',
  uses           TEXT NOT NULL,       -- JSON array; part of the interval's identity
  dev_name       TEXT NOT NULL DEFAULT '',
  first_seen     INTEGER NOT NULL,
  last_seen      INTEGER NOT NULL,    -- last heartbeat that confirmed it
  ended_at       INTEGER,             -- first report that contradicted it; NULL = current
  end_reason     TEXT NOT NULL DEFAULT ''  -- vanished|moved|use_changed|merged
);
CREATE INDEX placement_drive ON placement(drive_id, first_seen);
CREATE INDEX placement_open ON placement(host_id, drive_id) WHERE ended_at IS NULL;

-- The single ledger. Manual declarations are events with source 'user:…'.
CREATE TABLE event (
  event_id       INTEGER PRIMARY KEY,
  ts             INTEGER NOT NULL,
  kind           TEXT NOT NULL,
  drive_id       INTEGER REFERENCES drive(drive_id),
  host_id        INTEGER REFERENCES host(host_id),
  detail         TEXT NOT NULL DEFAULT '{}',
  source         TEXT NOT NULL,
  snapshot_id    INTEGER REFERENCES snapshot(snapshot_id)
);
CREATE INDEX event_drive ON event(drive_id, ts);
CREATE INDEX event_ts ON event(ts);
CREATE INDEX event_kind ON event(kind, ts);
