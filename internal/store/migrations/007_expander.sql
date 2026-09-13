-- Placements are keyed on a stable expander identity (its SAS address) and
-- keep the kernel's current name beside it, the way dev_name is kept.
-- Rows from before this migration carry the kernel name as both.
ALTER TABLE placement ADD COLUMN expander_dev TEXT NOT NULL DEFAULT '';
UPDATE placement SET expander_dev = expander;
ALTER TABLE snapshot_device ADD COLUMN expander_id TEXT NOT NULL DEFAULT '';

-- What a person calls an expander. Keyed on the placement key, so a name
-- given to a SAS address follows the shelf between hosts.
CREATE TABLE expander_name (
  expander       TEXT PRIMARY KEY,
  name           TEXT NOT NULL,
  note           TEXT NOT NULL DEFAULT '',
  set_by         TEXT NOT NULL DEFAULT '',
  set_at         INTEGER NOT NULL
);
