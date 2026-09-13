-- Latency sums may now cover fewer completions than the throughput counts:
-- the agent drops an interval's latency when a time counter jumped by the
-- host's uptime (a kernel accounting bug). Rows written before this
-- migration covered every completion.
ALTER TABLE io_sample ADD COLUMN await_reads INTEGER NOT NULL DEFAULT 0;
ALTER TABLE io_sample ADD COLUMN await_writes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE io_sample ADD COLUMN glitches INTEGER NOT NULL DEFAULT 0;
UPDATE io_sample SET await_reads = reads, await_writes = writes;
ALTER TABLE io_daily ADD COLUMN await_reads INTEGER NOT NULL DEFAULT 0;
ALTER TABLE io_daily ADD COLUMN await_writes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE io_daily ADD COLUMN glitches INTEGER NOT NULL DEFAULT 0;
UPDATE io_daily SET await_reads = reads, await_writes = writes;
