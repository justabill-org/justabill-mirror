-- CRS bill summaries from Congress.gov, every version (docs/design/197-crs-summaries.md, item 1).
-- Keyed on CRS's own (bill, versionCode). Interleaved without PARENT so a summary can be stored
-- before its bill is synced; the bill page shows it once the bill arrives. text_html is the text
-- as published (HTML, possibly invalid) and is never served; text is the plain-text rendering the
-- API serves. content_hash is the sha256 of text_html. source_updated_at is Congress.gov's
-- updateDate, which drives the sync's watermark; crs_updated_at is lastSummaryUpdateDate.
CREATE TABLE bill_crs_summaries (
  bill_id STRING(MAX) NOT NULL,
  version_code STRING(8) NOT NULL,
  action_date DATE NOT NULL,
  action_desc STRING(MAX) NOT NULL,
  chamber STRING(16),
  text_html STRING(MAX) NOT NULL,
  text STRING(MAX) NOT NULL,
  content_hash STRING(64) NOT NULL,
  crs_updated_at TIMESTAMP NOT NULL,
  source_updated_at TIMESTAMP NOT NULL,
  synced_at TIMESTAMP NOT NULL OPTIONS (allow_commit_timestamp = true),
) PRIMARY KEY (bill_id, version_code),
  INTERLEAVE IN bills;
