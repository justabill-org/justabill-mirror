-- Federal Register documents that CRA resolutions disapprove, or that the disapproved rules
-- withdraw (docs/design/590-cra-disapproved-rules.md). Keyed on the Federal Register's own
-- document number, so companion resolutions share a row. Text fields are plain text as published
-- (entities unescaped once at ingest) and are never served as HTML. html_url is on
-- www.federalregister.gov and pdf_url on www.govinfo.gov (the official edition); any other host is
-- dropped at ingest. content_hash is the sha256 of the other columns, so an unchanged document
-- isn't rewritten.
CREATE TABLE federal_register_documents (
  document_number STRING(32) NOT NULL,
  citation STRING(32) NOT NULL,
  volume INT64 NOT NULL,
  start_page INT64 NOT NULL,
  end_page INT64 NOT NULL,
  doc_type STRING(32) NOT NULL,
  action STRING(MAX),
  title STRING(MAX) NOT NULL,
  agencies JSON NOT NULL,
  publication_date DATE NOT NULL,
  effective_on DATE,
  abstract STRING(MAX),
  html_url STRING(MAX) NOT NULL,
  pdf_url STRING(MAX),
  docket_id STRING(MAX),
  regulation_id_numbers JSON,
  content_hash STRING(64) NOT NULL,
  fetched_at TIMESTAMP NOT NULL OPTIONS (allow_commit_timestamp = true),
) PRIMARY KEY (document_number);

GRANT SELECT ON TABLE federal_register_documents TO ROLE api;
