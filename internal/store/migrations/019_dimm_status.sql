-- A person's status for a memory module, keyed by the module rather than
-- the slot it sits in so it follows the module to another slot or host.
-- Modules with no usable serial can't be keyed and so can't be marked.
-- No row means ok.
CREATE TABLE dimm_status (
  part           TEXT NOT NULL,
  serial         TEXT NOT NULL,
  status         TEXT NOT NULL DEFAULT 'ok',  -- ok|suspect|bad|shelved|retired, as for drives
  PRIMARY KEY (part, serial)
);
