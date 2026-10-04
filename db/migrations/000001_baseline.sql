-- Spanner DDL for Just a Bill (GoogleSQL dialect)
-- Replaces all PostgreSQL migrations/*.sql files
--
-- Interleaving: child PK prefix must use the SAME column names as parent PK.
-- Parent PKs are named descriptively (bill_id, vote_id, user_id) so children
-- can naturally share the column name.

CREATE TABLE congresses (
  number INT64 NOT NULL,
  start_date DATE NOT NULL,
  end_date DATE,
  is_current BOOL NOT NULL DEFAULT (false),
) PRIMARY KEY (number);

CREATE TABLE members (
  bioguide_id STRING(36) NOT NULL,
  first_name STRING(MAX) NOT NULL,
  last_name STRING(MAX) NOT NULL,
  birth_year INT64,
  photo_url STRING(MAX),
  official_url STRING(MAX),
  lis_id STRING(36),
) PRIMARY KEY (bioguide_id);

CREATE UNIQUE NULL_FILTERED INDEX idx_members_lis_id ON members(lis_id);

CREATE TABLE member_terms (
  member_id STRING(36) NOT NULL,
  congress INT64 NOT NULL,
  chamber STRING(MAX) NOT NULL,
  state STRING(2) NOT NULL,
  district INT64,
  party STRING(1) NOT NULL,
  start_date DATE,
  end_date DATE,
  CONSTRAINT fk_member_terms_member FOREIGN KEY (member_id) REFERENCES members(bioguide_id),
  CONSTRAINT fk_member_terms_congress FOREIGN KEY (congress) REFERENCES congresses(number),
) PRIMARY KEY (member_id, congress, chamber);

CREATE INDEX idx_member_terms_district ON member_terms(state, district, congress, chamber);
CREATE INDEX idx_member_terms_congress_chamber ON member_terms(congress, chamber);

-- Parent PK: bill_id (not "id") so interleaved children can share the name.
CREATE TABLE bills (
  bill_id STRING(MAX) NOT NULL,
  congress INT64 NOT NULL,
  bill_type STRING(MAX) NOT NULL,
  number INT64 NOT NULL,
  title STRING(MAX) NOT NULL,
  introduced_date DATE,
  origin_chamber STRING(MAX),
  latest_action JSON,
  current_status STRING(MAX),
  status_date DATE,
  policy_area STRING(MAX),
  sponsors JSON,
  cosponsors JSON,
  committees JSON,
  subjects JSON,
  related_bills JSON,
  -- Slug of policy_area (e.g. "armed-forces-and-national-security"); key of policy_areas.
  policy_area_id STRING(MAX) AS (TRIM(REGEXP_REPLACE(LOWER(policy_area), r'[^a-z0-9]+', '-'), '-')) STORED,
  updated_at TIMESTAMP,
  synced_at TIMESTAMP DEFAULT (CURRENT_TIMESTAMP()),
  title_tokens TOKENLIST AS (TOKENIZE_FULLTEXT(title)) HIDDEN,
  CONSTRAINT fk_bills_congress FOREIGN KEY (congress) REFERENCES congresses(number),
) PRIMARY KEY (bill_id);

CREATE UNIQUE INDEX idx_bills_congress_type_number ON bills(congress, bill_type, number);
CREATE INDEX idx_bills_congress ON bills(congress);
CREATE INDEX idx_bills_status ON bills(current_status);
CREATE SEARCH INDEX idx_bills_title_search ON bills(title_tokens);
CREATE INDEX idx_bills_policy_area ON bills(policy_area_id);

-- Ontology object tables (docs/design/31-bill-ontology.md). Nodes other bills link to.
-- committee_id is the Congress.gov systemCode, lowercased (e.g. "hswm00").
CREATE TABLE committees (
  committee_id STRING(MAX) NOT NULL,
  name STRING(MAX) NOT NULL,
  chamber STRING(MAX),
  committee_type STRING(MAX),
  synced_at TIMESTAMP DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (committee_id);

-- Legislative subjects; subject_id is the slug of name.
CREATE TABLE subjects (
  subject_id STRING(MAX) NOT NULL,
  name STRING(MAX) NOT NULL,
) PRIMARY KEY (subject_id);

-- Policy areas; policy_area_id is the slug of name, matching bills.policy_area_id.
CREATE TABLE policy_areas (
  policy_area_id STRING(MAX) NOT NULL,
  name STRING(MAX) NOT NULL,
) PRIMARY KEY (policy_area_id);

-- Ontology link tables, interleaved in bills with an index for reverse traversal.
-- FKs to members and to related bills are NOT ENFORCED: the other end may not be synced yet,
-- and an enforced FK would fail the whole write.

-- SPONSORED: role is 'sponsor' or 'cosponsor'.
CREATE TABLE bill_sponsorships (
  bill_id STRING(MAX) NOT NULL,
  member_id STRING(36) NOT NULL,
  role STRING(MAX) NOT NULL,
  sponsored_date DATE,
  is_original BOOL,
  CONSTRAINT fk_bill_sponsorships_member FOREIGN KEY (member_id) REFERENCES members(bioguide_id) NOT ENFORCED,
) PRIMARY KEY (bill_id, member_id),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;

CREATE INDEX idx_bill_sponsorships_member ON bill_sponsorships(member_id, role);

-- REFERRED_TO: one row per committee activity ("Referred To", "Markup By", ...).
CREATE TABLE bill_committees (
  bill_id STRING(MAX) NOT NULL,
  committee_id STRING(MAX) NOT NULL,
  activity STRING(MAX) NOT NULL,
  activity_date TIMESTAMP,
  CONSTRAINT fk_bill_committees_committee FOREIGN KEY (committee_id) REFERENCES committees(committee_id),
) PRIMARY KEY (bill_id, committee_id, activity),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;

CREATE INDEX idx_bill_committees_committee ON bill_committees(committee_id);

-- ABOUT
CREATE TABLE bill_subjects (
  bill_id STRING(MAX) NOT NULL,
  subject_id STRING(MAX) NOT NULL,
  CONSTRAINT fk_bill_subjects_subject FOREIGN KEY (subject_id) REFERENCES subjects(subject_id),
) PRIMARY KEY (bill_id, subject_id),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;

CREATE INDEX idx_bill_subjects_subject ON bill_subjects(subject_id);

-- RELATED_TO: related_bill_id is built like bill_id (<type>-<congress>-<number>).
CREATE TABLE bill_relations (
  bill_id STRING(MAX) NOT NULL,
  related_bill_id STRING(MAX) NOT NULL,
  relation_type STRING(MAX) NOT NULL,
  identified_by STRING(MAX),
  CONSTRAINT fk_bill_relations_related FOREIGN KEY (related_bill_id) REFERENCES bills(bill_id) NOT ENFORCED,
) PRIMARY KEY (bill_id, related_bill_id, relation_type),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;

CREATE INDEX idx_bill_relations_related ON bill_relations(related_bill_id);

-- Interleaved in bills: structured audit trail of every lifecycle stage reached.
-- Derived from bill_actions at sync time. Used for journey view and "ever reached" filters.
CREATE TABLE bill_status_history (
  bill_id STRING(MAX) NOT NULL,
  status STRING(MAX) NOT NULL,
  status_date DATE NOT NULL,
  status_rank INT64 NOT NULL,
) PRIMARY KEY (bill_id, status),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;

CREATE INDEX idx_bill_status_history_status ON bill_status_history(status);

-- Interleaved in bills
CREATE TABLE bill_actions (
  bill_id STRING(MAX) NOT NULL,
  action_id STRING(36) NOT NULL DEFAULT (GENERATE_UUID()),
  action_date DATE NOT NULL,
  action_time STRING(MAX),
  action_text STRING(MAX) NOT NULL,
  action_type STRING(MAX),
  action_code STRING(MAX),
  source_system STRING(MAX),
  committee_code STRING(MAX),
  recorded_vote JSON,
  sort_order INT64 NOT NULL,
) PRIMARY KEY (bill_id, action_id),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;

CREATE UNIQUE INDEX idx_bill_actions_dedup ON bill_actions(bill_id, action_text, sort_order);

-- Interleaved in bills
CREATE TABLE bill_summaries (
  bill_id STRING(MAX) NOT NULL,
  short_summary STRING(MAX),
  long_summary STRING(MAX),
  why_it_matters STRING(MAX),
  model_used STRING(MAX),
  generated_at TIMESTAMP DEFAULT (CURRENT_TIMESTAMP()),
  summary_tokens TOKENLIST AS (TOKENIZE_FULLTEXT(COALESCE(short_summary, '') || ' ' || COALESCE(long_summary, ''))) HIDDEN,
) PRIMARY KEY (bill_id),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;

CREATE SEARCH INDEX idx_bill_summaries_search ON bill_summaries(summary_tokens);

-- Interleaved in bills
CREATE TABLE bill_text_versions (
  bill_id STRING(MAX) NOT NULL,
  version_id STRING(36) NOT NULL DEFAULT (GENERATE_UUID()),
  version_type STRING(MAX) NOT NULL,
  version_code STRING(MAX) NOT NULL,
  date DATE,
  formats JSON,
  sort_order INT64 NOT NULL,
  synced_at TIMESTAMP DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (bill_id, version_id),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;

CREATE UNIQUE INDEX idx_btv_bill_version_code ON bill_text_versions(bill_id, version_code);
CREATE INDEX idx_btv_id ON bill_text_versions(version_id);

-- NOT interleaved: referenced by version_id across parents
CREATE TABLE bill_texts (
  text_id STRING(36) NOT NULL DEFAULT (GENERATE_UUID()),
  version_id STRING(36) NOT NULL,
  format STRING(MAX) NOT NULL,
  content STRING(MAX) NOT NULL,
  content_hash STRING(MAX) NOT NULL,
  sections JSON,
  fetched_at TIMESTAMP,
) PRIMARY KEY (text_id);

CREATE UNIQUE INDEX idx_bill_texts_version ON bill_texts(version_id);

-- Interleaved in bills
CREATE TABLE bill_text_diffs (
  bill_id STRING(MAX) NOT NULL,
  diff_id STRING(36) NOT NULL DEFAULT (GENERATE_UUID()),
  from_version_id STRING(36) NOT NULL,
  to_version_id STRING(36) NOT NULL,
  diff_stats JSON,
  diff_content JSON NOT NULL,
  generated_at TIMESTAMP,
) PRIMARY KEY (bill_id, diff_id),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;

CREATE UNIQUE INDEX idx_btd_versions ON bill_text_diffs(from_version_id, to_version_id);
CREATE INDEX idx_btd_id ON bill_text_diffs(diff_id);

-- NOT interleaved: referenced by diff_id
CREATE TABLE bill_text_diff_summaries (
  diff_id STRING(36) NOT NULL,
  summary STRING(MAX) NOT NULL,
  model_used STRING(MAX),
  generated_at TIMESTAMP DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (diff_id);

-- Interleaved in bills
CREATE TABLE amendments (
  bill_id STRING(MAX) NOT NULL,
  amendment_id STRING(MAX) NOT NULL,
  congress INT64 NOT NULL,
  amendment_type STRING(MAX) NOT NULL,
  amendment_number INT64 NOT NULL,
  description STRING(MAX),
  purpose STRING(MAX),
  sponsor_id STRING(36),
  latest_action JSON,
  submitted_date DATE,
  chamber STRING(MAX) NOT NULL,
  synced_at TIMESTAMP DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (bill_id, amendment_id),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;

-- Parent PK: vote_id (not "id") so member_votes can interleave.
CREATE TABLE congressional_votes (
  vote_id STRING(MAX) NOT NULL,
  bill_id STRING(MAX),
  congress INT64 NOT NULL,
  chamber STRING(MAX) NOT NULL,
  session INT64,
  roll_number INT64,
  vote_date TIMESTAMP NOT NULL,
  question STRING(MAX),
  result STRING(MAX),
  yeas INT64,
  nays INT64,
  present INT64,
  not_voting INT64,
  CONSTRAINT fk_cv_congress FOREIGN KEY (congress) REFERENCES congresses(number),
) PRIMARY KEY (vote_id);

CREATE INDEX idx_cv_bill ON congressional_votes(bill_id);
CREATE INDEX idx_cv_congress ON congressional_votes(congress);

-- Interleaved in congressional_votes
CREATE TABLE member_votes (
  vote_id STRING(MAX) NOT NULL,
  member_id STRING(36) NOT NULL,
  vote STRING(MAX) NOT NULL,
) PRIMARY KEY (vote_id, member_id),
  INTERLEAVE IN PARENT congressional_votes ON DELETE CASCADE;

-- Parent PK: user_id (not "id") so user_votes/favorites can interleave.
-- auth_uid is the identity provider's opaque subject (Identity Platform uid).
-- No email, name or street address is stored (docs/design/55-social-auth.md).
CREATE TABLE users (
  user_id STRING(36) NOT NULL,
  auth_uid STRING(128) NOT NULL,
  state STRING(2),
  district INT64,
  created_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (user_id);

CREATE UNIQUE INDEX idx_users_auth_uid ON users(auth_uid);

-- Interleaved in users
CREATE TABLE user_votes (
  user_id STRING(36) NOT NULL,
  bill_id STRING(MAX) NOT NULL,
  vote STRING(MAX) NOT NULL,
  voted_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (user_id, bill_id),
  INTERLEAVE IN PARENT users ON DELETE CASCADE;

-- Interleaved in users
CREATE TABLE user_favorites (
  user_id STRING(36) NOT NULL,
  bill_id STRING(MAX) NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (user_id, bill_id),
  INTERLEAVE IN PARENT users ON DELETE CASCADE;

-- GAO (Government Accountability Office) reports.
-- NOT interleaved: a single report can relate to multiple bills.
CREATE TABLE gao_reports (
  report_id STRING(MAX) NOT NULL,
  title STRING(MAX) NOT NULL,
  report_number STRING(MAX),
  report_type STRING(MAX),
  published_date DATE,
  summary STRING(MAX),
  pdf_url STRING(MAX),
  html_url STRING(MAX),
  synced_at TIMESTAMP DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (report_id);

CREATE INDEX idx_gao_reports_number ON gao_reports(report_number);

-- Junction: links bills to related GAO reports. Interleaved for co-located reads.
CREATE TABLE bill_gao_reports (
  bill_id STRING(MAX) NOT NULL,
  report_id STRING(MAX) NOT NULL,
  linked_at TIMESTAMP NOT NULL DEFAULT (CURRENT_TIMESTAMP()),
) PRIMARY KEY (bill_id, report_id),
  INTERLEAVE IN PARENT bills ON DELETE CASCADE;

-- Composite PK, no SERIAL id
CREATE TABLE sync_state (
  step STRING(MAX) NOT NULL,
  congress INT64 NOT NULL,
  last_synced_at TIMESTAMP NOT NULL,
  last_offset STRING(MAX),
  items_synced INT64 NOT NULL DEFAULT (0),
  error_count INT64 NOT NULL DEFAULT (0),
  last_error STRING(MAX),
) PRIMARY KEY (step, congress);

-- Ontology (docs/design/31-bill-ontology.md): a property graph over the tables above. It is a
-- view, not a copy, so it holds no data of its own and dropping it leaves the tables intact.
-- Only public congressional data: users, user_votes and user_favorites stay out of the graph.
-- Edges whose far end isn't synced yet (NOT ENFORCED FKs, NULL bill_id or sponsor_id) don't match.
CREATE PROPERTY GRAPH civic_graph
  NODE TABLES (
    bills KEY (bill_id) LABEL Bill
      PROPERTIES (bill_id, congress, bill_type, number, title, introduced_date, origin_chamber,
        current_status, status_date, policy_area_id),
    members KEY (bioguide_id) LABEL Member
      PROPERTIES (bioguide_id, first_name, last_name),
    congresses KEY (number) LABEL Congress
      PROPERTIES (number, start_date, end_date, is_current),
    committees KEY (committee_id) LABEL Committee
      PROPERTIES (committee_id, name, chamber, committee_type),
    subjects KEY (subject_id) LABEL Subject
      PROPERTIES (subject_id, name),
    policy_areas KEY (policy_area_id) LABEL PolicyArea
      PROPERTIES (policy_area_id, name),
    congressional_votes KEY (vote_id) LABEL RollCall
      PROPERTIES (vote_id, congress, chamber, session, roll_number, vote_date, question, result,
        yeas, nays, present, not_voting),
    amendments KEY (bill_id, amendment_id) LABEL Amendment
      PROPERTIES (amendment_id, congress, amendment_type, amendment_number, description, purpose,
        submitted_date, chamber),
    gao_reports KEY (report_id) LABEL GaoReport
      PROPERTIES (report_id, title, report_number, report_type, published_date)
  )
  EDGE TABLES (
    bill_sponsorships KEY (bill_id, member_id)
      SOURCE KEY (member_id) REFERENCES members (bioguide_id)
      DESTINATION KEY (bill_id) REFERENCES bills (bill_id)
      LABEL SPONSORED PROPERTIES (role, sponsored_date, is_original),
    member_votes AS cast_votes KEY (vote_id, member_id)
      SOURCE KEY (member_id) REFERENCES members (bioguide_id)
      DESTINATION KEY (vote_id) REFERENCES congressional_votes (vote_id)
      LABEL CAST_VOTE PROPERTIES (vote),
    congressional_votes AS roll_call_on KEY (vote_id)
      SOURCE KEY (vote_id) REFERENCES congressional_votes (vote_id)
      DESTINATION KEY (bill_id) REFERENCES bills (bill_id)
      LABEL ON_BILL NO PROPERTIES,
    bill_relations KEY (bill_id, related_bill_id, relation_type)
      SOURCE KEY (bill_id) REFERENCES bills (bill_id)
      DESTINATION KEY (related_bill_id) REFERENCES bills (bill_id)
      LABEL RELATED_TO PROPERTIES (relation_type, identified_by),
    bill_committees KEY (bill_id, committee_id, activity)
      SOURCE KEY (bill_id) REFERENCES bills (bill_id)
      DESTINATION KEY (committee_id) REFERENCES committees (committee_id)
      LABEL REFERRED_TO PROPERTIES (activity, activity_date),
    bill_subjects KEY (bill_id, subject_id)
      SOURCE KEY (bill_id) REFERENCES bills (bill_id)
      DESTINATION KEY (subject_id) REFERENCES subjects (subject_id)
      LABEL ABOUT NO PROPERTIES,
    bills AS in_policy_area KEY (bill_id)
      SOURCE KEY (bill_id) REFERENCES bills (bill_id)
      DESTINATION KEY (policy_area_id) REFERENCES policy_areas (policy_area_id)
      LABEL IN_POLICY_AREA NO PROPERTIES,
    bills AS introduced_in KEY (bill_id)
      SOURCE KEY (bill_id) REFERENCES bills (bill_id)
      DESTINATION KEY (congress) REFERENCES congresses (number)
      LABEL INTRODUCED_IN NO PROPERTIES,
    amendments AS amends KEY (bill_id, amendment_id)
      SOURCE KEY (bill_id, amendment_id) REFERENCES amendments (bill_id, amendment_id)
      DESTINATION KEY (bill_id) REFERENCES bills (bill_id)
      LABEL AMENDS NO PROPERTIES,
    amendments AS sponsored_amendment KEY (bill_id, amendment_id)
      SOURCE KEY (sponsor_id) REFERENCES members (bioguide_id)
      DESTINATION KEY (bill_id, amendment_id) REFERENCES amendments (bill_id, amendment_id)
      LABEL SPONSORED_AMENDMENT NO PROPERTIES,
    member_terms AS served_in KEY (member_id, congress, chamber)
      SOURCE KEY (member_id) REFERENCES members (bioguide_id)
      DESTINATION KEY (congress) REFERENCES congresses (number)
      LABEL SERVED_IN PROPERTIES (chamber, state, district, party, start_date, end_date),
    bill_gao_reports AS cites KEY (bill_id, report_id)
      SOURCE KEY (bill_id) REFERENCES bills (bill_id)
      DESTINATION KEY (report_id) REFERENCES gao_reports (report_id)
      LABEL CITES PROPERTIES (linked_at)
  );
