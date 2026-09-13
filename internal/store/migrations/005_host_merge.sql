-- A host merged into another keeps its row so its machine id still
-- resolves; everything it owned has moved to the target.
ALTER TABLE host ADD COLUMN merged_into INTEGER REFERENCES host(host_id);
