-- Aggregates 2/8 (#120): how often the published majority of a member's constituency agreed with
-- the member's roll-call votes, per congress. Computed only from published vote_aggregates cells.
CREATE TABLE rep_alignment (
  member_id STRING(36) NOT NULL,
  congress INT64 NOT NULL,
  scope_key STRING(8) NOT NULL,
  bills_compared INT64 NOT NULL,
  bills_agreed INT64 NOT NULL,
  computed_at TIMESTAMP NOT NULL,
) PRIMARY KEY (member_id, congress, scope_key);
