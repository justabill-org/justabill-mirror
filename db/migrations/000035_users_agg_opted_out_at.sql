-- A signed-in user's own choice to leave their votes out of the published totals (#1040, plan
-- decision 7 on #982): set by PATCH /me count_in_totals=false, cleared by true. NULL means the
-- votes count. It's separate from agg_excluded_at, the operator's cohort flag, so the aggregates
-- CLI never reads or overwrites it. Nullable, so existing rows read as counted.
ALTER TABLE users ADD COLUMN agg_opted_out_at TIMESTAMP;
