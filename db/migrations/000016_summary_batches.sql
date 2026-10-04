-- Vertex AI batch jobs for whole-corpus re-summarization, and each attempt's request type and batch
-- (docs/design/198-corpus-resummarization.md, "Schema"). Additive: a new table and two nullable
-- columns that only the batch path reads.
--
-- state is exporting, a Vertex JOB_STATE_* value, imported or released. A bill exported to a batch
-- gets a batch_pending attempt with request_type 'batch' and the batch's id, which holds it out of
-- the synchronous queue until next_attempt_at.
CREATE TABLE summary_batches (
  batch_id STRING(36) NOT NULL,
  congress INT64 NOT NULL,
  model STRING(MAX) NOT NULL,
  prompt_version STRING(MAX) NOT NULL,
  job_name STRING(MAX),
  state STRING(MAX) NOT NULL,
  bill_count INT64 NOT NULL,
  input_uri STRING(MAX) NOT NULL,
  output_uri STRING(MAX),
  ok_count INT64,
  failed_count INT64,
  created_at TIMESTAMP NOT NULL,
  finished_at TIMESTAMP,
  imported_at TIMESTAMP,
) PRIMARY KEY (batch_id);

ALTER TABLE summary_attempts ADD COLUMN request_type STRING(MAX);

ALTER TABLE summary_attempts ADD COLUMN batch_id STRING(36);
