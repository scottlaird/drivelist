-- ECC three ways: the module's widths (check bits beyond the data
-- width), the firmware's error correction for the array, and the mode
-- EDAC reports; a module with check bits on an array with none is ECC
-- fitted but not on.
ALTER TABLE dimm ADD COLUMN total_width INTEGER NOT NULL DEFAULT 0;
ALTER TABLE dimm ADD COLUMN data_width INTEGER NOT NULL DEFAULT 0;
ALTER TABLE dimm ADD COLUMN edac_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE host ADD COLUMN mem_correction TEXT NOT NULL DEFAULT '';
