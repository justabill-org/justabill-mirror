-- The API's database role (#609, docs/design/609-api-database-role.md). The API connects as
-- `api` (SPANNER_DATABASE_ROLE); Spanner then refuses any write outside the user tables.
-- Every table is classified in apirole_test.go (TestAPIRoleGrants); a migration that adds a
-- table grants the role access to it in the same file, or lists it in the test as one the API
-- never reads. A DROP TABLE must REVOKE the table's grants first.
-- The emulator accepts these statements but doesn't enforce them, and its DDL dump leaves roles
-- and grants out, so db/schema.sql doesn't show them. The bookkeeping tables (sync_state,
-- sync_retry, pipeline_leases, the *_attempts tables, summary_batches) get no grant.
CREATE ROLE api;

GRANT SELECT ON TABLE congresses, members, member_terms, bills, committees, subjects,
  policy_areas, bill_sponsorships, bill_committees, bill_subjects, bill_relations,
  bill_status_history, bill_actions, bill_summaries, bill_text_versions, bill_texts,
  bill_text_diffs, bill_text_diff_summaries, amendments, congressional_votes, member_votes,
  gao_reports, bill_gao_reports, vote_aggregates, rep_alignment, usc_release_points,
  usc_sections, bill_law_refs, bill_law_changes, bill_crs_summaries TO ROLE api;

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE users, user_votes, user_favorites TO ROLE api;
