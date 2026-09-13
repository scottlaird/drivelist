-- Placements are keyed on the physical enclosure (its SES identifier)
-- rather than the expander. Existing keys are expander addresses, which
-- for most hardware equal the enclosure identifier; where they differ the
-- next report from an upgraded agent renames the enclosure in place.
ALTER TABLE placement RENAME COLUMN expander TO enclosure;
ALTER TABLE placement RENAME COLUMN expander_dev TO enclosure_via;
ALTER TABLE expander_name RENAME TO enclosure_name;
ALTER TABLE enclosure_name RENAME COLUMN expander TO enclosure;
ALTER TABLE snapshot_device ADD COLUMN enclosure_id TEXT NOT NULL DEFAULT '';
ALTER TABLE snapshot_device ADD COLUMN enclosure_via TEXT NOT NULL DEFAULT '';
ALTER TABLE snapshot_device ADD COLUMN enclosure_via_id TEXT NOT NULL DEFAULT '';

-- Every enclosure a host has reported, drives or not, with the bays seen
-- in it (empty ones included), so an empty shelf can be listed and named.
CREATE TABLE enclosure (
  host_id        INTEGER NOT NULL REFERENCES host(host_id),
  enclosure      TEXT NOT NULL,       -- the placement key
  via            TEXT NOT NULL DEFAULT '',
  via_address    TEXT NOT NULL DEFAULT '',
  bays           INTEGER NOT NULL DEFAULT 0,
  first_seen     INTEGER NOT NULL,
  last_seen      INTEGER NOT NULL,
  PRIMARY KEY (host_id, enclosure)
);
