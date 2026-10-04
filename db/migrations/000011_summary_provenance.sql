-- Summary provenance (docs/design/68-ai-summaries-gemini-3.md, "Schema changes"). A bill summary
-- records the text it was generated from, so the queue can re-summarize a bill when its latest
-- text's content_hash changes, plus the prompt, served model version and token usage. Existing
-- summaries keep NULLs, count as stale and are regenerated once under the new prompt.
ALTER TABLE bill_summaries ADD COLUMN source_version_id STRING(36);

ALTER TABLE bill_summaries ADD COLUMN source_version_code STRING(MAX);

ALTER TABLE bill_summaries ADD COLUMN source_content_hash STRING(MAX);

ALTER TABLE bill_summaries ADD COLUMN prompt_version STRING(MAX);

ALTER TABLE bill_summaries ADD COLUMN model_version STRING(MAX);

ALTER TABLE bill_summaries ADD COLUMN input_truncated BOOL;

ALTER TABLE bill_summaries ADD COLUMN input_tokens INT64;

ALTER TABLE bill_summaries ADD COLUMN output_tokens INT64;

ALTER TABLE bill_summaries ADD COLUMN thinking_tokens INT64;

ALTER TABLE bill_text_diff_summaries ADD COLUMN prompt_version STRING(MAX);

ALTER TABLE bill_text_diff_summaries ADD COLUMN model_version STRING(MAX);

ALTER TABLE bill_text_diff_summaries ADD COLUMN input_tokens INT64;

ALTER TABLE bill_text_diff_summaries ADD COLUMN output_tokens INT64;
