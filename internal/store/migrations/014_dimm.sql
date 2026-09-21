-- Memory modules per host as the agent last reported them (SMBIOS type
-- 17 joined to EDAC), with EDAC's cumulative error counts. Reports
-- update rows in place; growth becomes samples and events.
CREATE TABLE dimm (
  host_id        INTEGER NOT NULL REFERENCES host(host_id),
  key            TEXT NOT NULL,             -- the slot, else the EDAC location
  slot           TEXT NOT NULL DEFAULT '',
  bank           TEXT NOT NULL DEFAULT '',
  size_bytes     INTEGER NOT NULL DEFAULT 0,
  ranks          INTEGER NOT NULL DEFAULT 0,
  type           TEXT NOT NULL DEFAULT '',
  speed_mts      INTEGER NOT NULL DEFAULT 0,
  manufacturer   TEXT NOT NULL DEFAULT '',
  part           TEXT NOT NULL DEFAULT '',
  serial         TEXT NOT NULL DEFAULT '',
  edac           TEXT NOT NULL DEFAULT '',  -- "mc0/csrow2/ch2+mc0/csrow3/ch2"
  edac_type      TEXT NOT NULL DEFAULT '',
  mapping        TEXT NOT NULL DEFAULT '',
  ce             INTEGER NOT NULL DEFAULT 0, -- since boot
  ue             INTEGER NOT NULL DEFAULT 0,
  first_seen     INTEGER NOT NULL,
  last_seen      INTEGER NOT NULL,
  gone_at        INTEGER,                    -- NULL while present
  PRIMARY KEY (host_id, key)
);

-- Growth of a module's counts between two reports.
CREATE TABLE dimm_sample (
  host_id        INTEGER NOT NULL REFERENCES host(host_id),
  key            TEXT NOT NULL,
  ts             INTEGER NOT NULL,
  ce             INTEGER NOT NULL DEFAULT 0,
  ue             INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (host_id, key, ts)
);

-- The boot the counts were last read in; a new boot restarts them.
ALTER TABLE host ADD COLUMN dimm_boot_id TEXT NOT NULL DEFAULT '';
