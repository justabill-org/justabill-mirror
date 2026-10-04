-- Aggregates 2/8 (#120): published per-bill aggregates, one row per bill and scope (national, a
-- state or a district). The aggregation job writes it; the API only ever reads this table, never
-- user_votes. basis_yea, basis_nay and hold_reason are job bookkeeping and never served.
CREATE TABLE vote_aggregates (
  bill_id STRING(MAX) NOT NULL,
  scope STRING(16) NOT NULL,
  scope_key STRING(8) NOT NULL,
  status STRING(16) NOT NULL,
  yea_pct INT64,
  nay_pct INT64,
  voters_floor INT64,
  published_at TIMESTAMP,
  basis_yea INT64,
  basis_nay INT64,
  hold_reason STRING(32),
  computed_at TIMESTAMP NOT NULL,
) PRIMARY KEY (bill_id, scope, scope_key),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;
