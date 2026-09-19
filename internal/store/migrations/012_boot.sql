-- The kernel's boot id and boot time, from agents that send them: a new
-- boot id is a reboot, whether or not the host was silent long enough
-- to go stale. sas_boot_id is the boot the SAS counters were last read
-- in, so a report from a new boot restarts the counters instead of
-- reading their reset as growth.
ALTER TABLE host ADD COLUMN boot_id TEXT NOT NULL DEFAULT '';
ALTER TABLE host ADD COLUMN booted_at INTEGER;
ALTER TABLE host ADD COLUMN sas_boot_id TEXT NOT NULL DEFAULT '';
