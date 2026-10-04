-- sync_state tells the truth (docs/design/80-pipeline-operability.md, Decision 3A).
-- last_synced_at is the watermark: the start time of the last complete, successful, unlimited
-- run. It becomes nullable so a failure can be recorded before a step has ever succeeded.
-- consecutive_failures resets on success; error_count, last_error and last_error_at survive it.
ALTER TABLE sync_state ALTER COLUMN last_synced_at TIMESTAMP;

ALTER TABLE sync_state ADD COLUMN last_attempt_at TIMESTAMP;

ALTER TABLE sync_state ADD COLUMN last_error_at TIMESTAMP;

ALTER TABLE sync_state ADD COLUMN consecutive_failures INT64 NOT NULL DEFAULT (0);
