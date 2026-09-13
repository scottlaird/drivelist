-- The SAS topology per host as the agent last reported it: nodes (HBAs
-- and expanders) and their phys, with the kernel's cumulative error
-- counters. Reports update rows in place; changes become events.
CREATE TABLE sas_node (
  host_id        INTEGER NOT NULL REFERENCES host(host_id),
  address        TEXT NOT NULL,
  kind           TEXT NOT NULL,
  name           TEXT NOT NULL,
  vendor         TEXT NOT NULL DEFAULT '',
  product        TEXT NOT NULL DEFAULT '',
  revision       TEXT NOT NULL DEFAULT '',
  parent_address TEXT NOT NULL DEFAULT '',
  upstream_port  TEXT NOT NULL DEFAULT '',
  first_seen     INTEGER NOT NULL,
  last_seen      INTEGER NOT NULL,
  gone_at        INTEGER,             -- NULL while present
  PRIMARY KEY (host_id, address)
);

CREATE TABLE sas_phy (
  host_id          INTEGER NOT NULL REFERENCES host(host_id),
  owner_address    TEXT NOT NULL,
  phy_id           INTEGER NOT NULL,
  name             TEXT NOT NULL,
  port             TEXT NOT NULL DEFAULT '',
  port_width       INTEGER NOT NULL DEFAULT 0,
  rate             TEXT NOT NULL DEFAULT '',
  rate_gbit        REAL NOT NULL DEFAULT 0,
  attached_kind    TEXT NOT NULL DEFAULT '',
  attached         TEXT NOT NULL DEFAULT '',
  attached_address TEXT NOT NULL DEFAULT '',
  dev_name         TEXT NOT NULL DEFAULT '',
  bay              TEXT NOT NULL DEFAULT '',
  drive_id         INTEGER REFERENCES drive(drive_id),   -- the drive behind it, when known
  enabled          INTEGER NOT NULL DEFAULT 1,
  invalid_dword    INTEGER NOT NULL DEFAULT 0,   -- cumulative, as last reported
  disparity_error  INTEGER NOT NULL DEFAULT 0,
  loss_dword_sync  INTEGER NOT NULL DEFAULT 0,
  phy_reset_problem INTEGER NOT NULL DEFAULT 0,
  first_seen       INTEGER NOT NULL,
  last_seen        INTEGER NOT NULL,
  gone_at          INTEGER,
  PRIMARY KEY (host_id, owner_address, phy_id)
);

-- One row per report in which a phy's counters grew: the growth, not the
-- totals, so a window sums cleanly and a reboot (counters back to zero)
-- leaves no trace.
CREATE TABLE sas_phy_sample (
  host_id          INTEGER NOT NULL REFERENCES host(host_id),
  owner_address    TEXT NOT NULL,
  phy_id           INTEGER NOT NULL,
  ts               INTEGER NOT NULL,
  drive_id         INTEGER REFERENCES drive(drive_id),
  invalid_dword    INTEGER NOT NULL DEFAULT 0,
  disparity_error  INTEGER NOT NULL DEFAULT 0,
  loss_dword_sync  INTEGER NOT NULL DEFAULT 0,
  phy_reset_problem INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (host_id, owner_address, phy_id, ts)
);
CREATE INDEX sas_phy_sample_time ON sas_phy_sample(ts);
