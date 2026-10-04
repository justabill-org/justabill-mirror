-- gao_checked_at is when the pipeline last searched GovInfo for GAO reports that cite the bill
-- (docs/design/80-pipeline-operability.md, Decision 4A). It is set only after a successful
-- search, so the daily sync-gao job checks never-checked bills first, then re-checks each bill
-- every 30 days. NULL means never checked.
ALTER TABLE bills ADD COLUMN gao_checked_at TIMESTAMP;
