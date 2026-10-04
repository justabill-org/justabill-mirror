-- Current law and what bills change in it (docs/design/149-law-aware-assistant.md, item 1).
-- usc_sections holds the US Code at the latest Law Revision Counsel release point, one row per
-- section. bill_law_refs records each bill text version's references to it, parsed from the bill
-- XML, and bill_law_changes the plain-language explanation of each change for the latest text.
-- All of it is public-domain government data.
CREATE TABLE usc_release_points (
  release_point STRING(64) NOT NULL,
  published_date DATE,
  source_url STRING(MAX) NOT NULL,
  loaded_at TIMESTAMP,
  section_count INT64,
) PRIMARY KEY (release_point);

-- section_id is the USLM identifier with its en dash turned into a hyphen ("/us/usc/t42/s1395w-4"),
-- so it joins with the hyphenated citations in bill XML. section_number isn't numeric ("1395w-4").
-- status is current, repealed, omitted, transferred or reserved.
CREATE TABLE usc_sections (
  section_id STRING(200) NOT NULL,
  title_number INT64 NOT NULL,
  section_number STRING(64) NOT NULL,
  heading STRING(MAX),
  text STRING(MAX) NOT NULL,
  status STRING(32) NOT NULL,
  positive_law BOOL NOT NULL,
  release_point STRING(64) NOT NULL,
  content_hash STRING(64) NOT NULL,
  updated_at TIMESTAMP NOT NULL OPTIONS (allow_commit_timestamp = true),
  text_tokens TOKENLIST AS (TOKENIZE_FULLTEXT(COALESCE(heading, '') || ' ' || text)) HIDDEN,
) PRIMARY KEY (section_id);

CREATE SEARCH INDEX idx_usc_sections_search ON usc_sections(text_tokens);

-- One row per (text version, section, kind). section_id is a usc_sections id, or "nonusc:<cite>"
-- for a law the bill names without a US Code citation; those have no usc_sections row, and the
-- graph's CHANGES_LAW edge doesn't match them. ref_kind is amends, repeals, adds or cites.
CREATE TABLE bill_law_refs (
  bill_id STRING(MAX) NOT NULL,
  version_id STRING(36) NOT NULL,
  section_id STRING(200) NOT NULL,
  ref_kind STRING(16) NOT NULL,
  cite_text STRING(MAX),
  subsection_path STRING(200),
  instruction STRING(MAX),
  bill_section_ref STRING(200),
) PRIMARY KEY (bill_id, version_id, section_id, ref_kind),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;

CREATE INDEX idx_bill_law_refs_section ON bill_law_refs(section_id, ref_kind);

-- One explanation per bill and section, for the bill's latest text, with its provenance.
CREATE TABLE bill_law_changes (
  bill_id STRING(MAX) NOT NULL,
  section_id STRING(200) NOT NULL,
  change_kind STRING(16) NOT NULL,
  explanation STRING(MAX) NOT NULL,
  source_content_hash STRING(64) NOT NULL,
  release_point STRING(64) NOT NULL,
  model_used STRING(MAX) NOT NULL,
  prompt_version STRING(32) NOT NULL,
  generated_at TIMESTAMP NOT NULL OPTIONS (allow_commit_timestamp = true),
) PRIMARY KEY (bill_id, section_id),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;
