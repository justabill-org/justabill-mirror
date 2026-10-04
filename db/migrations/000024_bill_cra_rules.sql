-- One row per detected Congressional Review Act resolution: the rule it names, and the Federal
-- Register document matched to it, if any (docs/design/590-cra-disapproved-rules.md).
-- status is matched or unmatched; method (citation or title) is set when matched, reason
-- (no_candidates, ambiguous, cite_mismatch or unparsed) when unmatched. document_number and
-- withdrawn_document_number point at federal_register_documents. source_text_hash is the
-- bill_texts.content_hash the row was parsed from, NULL when parsed from the title alone;
-- matcher_version is the matcher's rules ("cra-v1"); either changing makes the bill due again.
-- context_hash is the sha256 of the summary prompt block's inputs.
CREATE TABLE bill_cra_rules (
  bill_id STRING(MAX) NOT NULL,
  rule_title STRING(MAX) NOT NULL,
  rule_agency STRING(MAX) NOT NULL,
  cited STRING(MAX),
  gao_opinion BOOL NOT NULL,
  status STRING(16) NOT NULL,
  method STRING(16),
  reason STRING(32),
  document_number STRING(32),
  withdrawn_document_number STRING(32),
  source_text_hash STRING(64),
  matcher_version STRING(16) NOT NULL,
  context_hash STRING(64) NOT NULL,
  checked_at TIMESTAMP NOT NULL OPTIONS (allow_commit_timestamp = true),
) PRIMARY KEY (bill_id),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;

GRANT SELECT ON TABLE bill_cra_rules TO ROLE api;
