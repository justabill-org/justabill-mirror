-- The latest summarization attempt per text diff, the diff counterpart of summary_attempts
-- (docs/design/68-ai-summaries-gemini-3.md, "Schema changes"; #440). A diff's content never
-- changes under its diff_id (a changed text deletes its diffs, and the sweep writes new ones with
-- new IDs), so the key replaces summary_attempts' bill_id and content_hash. It drives the same
-- retries (1 h, 4 h, 24 h), permanent blocks (next_attempt_at NULL for the same prompt_version
-- and model) and, counted over attempted_at, the shared daily request cap. Not interleaved, like
-- bill_text_diff_summaries: the code that deletes a diff deletes its attempt.
CREATE TABLE diff_summary_attempts (
  diff_id STRING(36) NOT NULL,
  prompt_version STRING(MAX) NOT NULL,
  model STRING(MAX) NOT NULL,
  outcome STRING(MAX) NOT NULL,
  reason STRING(MAX),
  attempts INT64 NOT NULL,
  attempted_at TIMESTAMP NOT NULL,
  next_attempt_at TIMESTAMP,
) PRIMARY KEY (diff_id);

CREATE INDEX idx_diff_summary_attempts_attempted_at ON diff_summary_attempts(attempted_at);
