-- The latest action's date as its own column (#760), so the bill list's sort=latest_action can
-- read it from an ordered index (idx_bills_list_latest_action, migration 29). It's exactly the
-- expression the list ordered by before, a YYYY-MM-DD string, so the order doesn't change, and
-- JSON_VALUE returns NULL (never an error) for a missing, null or malformed latest_action, so it
-- can't fail a bill write. Spanner fills it for the rows already there: nothing is refetched.
ALTER TABLE bills ADD COLUMN latest_action_date STRING(MAX)
  AS (JSON_VALUE(latest_action, '$.actionDate')) STORED;
