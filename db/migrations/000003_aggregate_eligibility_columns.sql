-- Aggregates 2/8 (#120, docs/design/89-aggregate-analytics.md): the account state and per-vote
-- flag the hourly aggregation job uses to decide which votes count. All nullable, so rows
-- written before this migration read as "unknown" and the running code is unaffected.
ALTER TABLE users ADD COLUMN sign_in_provider STRING(32);
ALTER TABLE users ADD COLUMN district_changed_at TIMESTAMP;
ALTER TABLE users ADD COLUMN agg_excluded_at TIMESTAMP;
ALTER TABLE user_votes ADD COLUMN app_check_ok BOOL;
