-- Network optics: pluggable modules in network ports, tracked like
-- drives. An optic is identified by vendor, part and serial; a module
-- with no serial is keyed by the host and port it was found in and so
-- cannot be followed when it moves. Its placements are the ports it
-- has been in; its samples are the last reading of each hour; events
-- about it carry its id.
CREATE TABLE optic (
  optic_id       INTEGER PRIMARY KEY,
  key            TEXT NOT NULL UNIQUE,      -- 'Mellanox|MMA1B00-C100D|MT1918FT01234', or 'port:<host_id>:<port>'
  form           TEXT NOT NULL DEFAULT '',
  identifier     TEXT NOT NULL DEFAULT '',
  kind           TEXT NOT NULL DEFAULT '',
  vendor         TEXT NOT NULL DEFAULT '',
  oui            TEXT NOT NULL DEFAULT '',
  part           TEXT NOT NULL DEFAULT '',
  rev            TEXT NOT NULL DEFAULT '',
  serial         TEXT NOT NULL DEFAULT '',
  date_code      TEXT NOT NULL DEFAULT '',
  compliance     TEXT NOT NULL DEFAULT '',
  connector      TEXT NOT NULL DEFAULT '',
  wavelength_nm  REAL NOT NULL DEFAULT 0,
  diagnostics    INTEGER NOT NULL DEFAULT 0,
  thresholds     TEXT NOT NULL DEFAULT '{}',  -- the module's own, from its EEPROM
  flags          TEXT NOT NULL DEFAULT '[]',  -- raised at the last report
  status         TEXT NOT NULL DEFAULT 'ok',  -- ok|suspect|bad|shelved|retired, as for drives
  first_seen     INTEGER NOT NULL,
  last_seen      INTEGER NOT NULL
);
CREATE INDEX optic_serial ON optic(serial);

CREATE TABLE optic_placement (
  placement_id   INTEGER PRIMARY KEY,
  optic_id       INTEGER NOT NULL REFERENCES optic(optic_id),
  host_id        INTEGER NOT NULL REFERENCES host(host_id),
  port           TEXT NOT NULL,
  ports          TEXT NOT NULL DEFAULT '',  -- breakout siblings, space separated
  link           TEXT NOT NULL DEFAULT '',
  first_seen     INTEGER NOT NULL,
  last_seen      INTEGER NOT NULL,
  ended_at       INTEGER,                   -- NULL while it is there
  end_reason     TEXT NOT NULL DEFAULT ''   -- vanished | moved | port
);
CREATE INDEX optic_placement_host ON optic_placement(host_id, ended_at);
CREATE INDEX optic_placement_optic ON optic_placement(optic_id, ended_at);

CREATE TABLE optic_sample (
  optic_id       INTEGER NOT NULL REFERENCES optic(optic_id),
  host_id        INTEGER NOT NULL REFERENCES host(host_id),
  hour           INTEGER NOT NULL,           -- unix time of the hour's start
  ts             INTEGER NOT NULL,           -- when the reading was taken
  port           TEXT NOT NULL DEFAULT '',
  temp_c         REAL,
  voltage_v      REAL,
  lanes          TEXT NOT NULL DEFAULT '[]', -- [{"lane":1,"bias_ma":6.7,"tx_mw":0.8,"rx_mw":0.75}]
  flags          TEXT NOT NULL DEFAULT '[]',
  link           TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (optic_id, hour)
);

ALTER TABLE event ADD COLUMN optic_id INTEGER REFERENCES optic(optic_id);
CREATE INDEX event_optic ON event(optic_id, ts);
