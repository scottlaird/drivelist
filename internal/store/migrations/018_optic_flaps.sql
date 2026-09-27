-- Link flaps per optic: the kernel's carrier_changes count last seen for
-- each placement, and how much it grew within each hour's sample.
ALTER TABLE optic_placement ADD COLUMN carrier_changes INTEGER;
ALTER TABLE optic_sample ADD COLUMN flaps INTEGER NOT NULL DEFAULT 0;
