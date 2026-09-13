-- What an enclosure is, when the agent can say (the chassis from DMI for
-- NVMe bays). Empty for SES enclosures, whose model comes from the SAS
-- node that reaches them.
ALTER TABLE enclosure ADD COLUMN product TEXT NOT NULL DEFAULT '';
