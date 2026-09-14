-- The DMI board name beside a chassis enclosure's model: vendors reuse a
-- product name ("Venus Series") across boards, and a hardware profile may
-- need the board to tell them apart.
ALTER TABLE enclosure ADD COLUMN board TEXT NOT NULL DEFAULT '';
