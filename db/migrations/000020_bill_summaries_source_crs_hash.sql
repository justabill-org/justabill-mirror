-- The CRS summary an AI summary was written with (docs/design/197-crs-summaries.md, item 4): the
-- content_hash of the bill's latest bill_crs_summaries row when the prompt carried it, or NULL
-- when it carried none. A bill is due again when its latest CRS summary's hash differs. Additive
-- and nullable, so the code already running keeps working.
ALTER TABLE bill_summaries ADD COLUMN source_crs_hash STRING(64);
