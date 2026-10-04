-- The latest law-change explanation attempt per bill (docs/design/149-law-aware-assistant.md,
-- "Explanations"), on the same contract as summary_attempts: it drives retries (next_attempt_at:
-- 1 h, 4 h, 24 h, then every 24 h), permanent blocks (next_attempt_at NULL for the same
-- content_hash, prompt_version and model) and the job's own daily cap, counted over attempted_at.
-- A bill's summary and its law-change explanation are separate calls, so they keep separate rows.
CREATE TABLE law_change_attempts (
  bill_id STRING(MAX) NOT NULL,
  content_hash STRING(MAX) NOT NULL,
  prompt_version STRING(MAX) NOT NULL,
  model STRING(MAX) NOT NULL,
  outcome STRING(MAX) NOT NULL,
  reason STRING(MAX),
  attempts INT64 NOT NULL,
  attempted_at TIMESTAMP NOT NULL,
  next_attempt_at TIMESTAMP,
) PRIMARY KEY (bill_id),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;

CREATE INDEX idx_law_change_attempts_attempted_at ON law_change_attempts(attempted_at);
