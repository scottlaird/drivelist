-- The kernel's MemTotal per host, and per module what the EDAC entries
-- matched to it add up to, so the module list can be checked against
-- what the kernel actually sees.
ALTER TABLE host ADD COLUMN mem_total_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE dimm ADD COLUMN edac_size_bytes INTEGER NOT NULL DEFAULT 0;
