-- Orphaned law references (#530): before the fix, pruning a bill text version
-- (UpsertBillTextVersions) left its bill_law_refs rows, so the bill kept showing as changing
-- the US Code sections that version referenced (CHANGES_LAW edges, BillsChangingSection). The
-- prune now deletes them in the same transaction; this removes the ones written before it.
--
-- Idempotent: a second run deletes nothing. Run it as normal DML, not Partitioned DML: a
-- partitioned statement may not read another table, and this one reads bill_text_versions
-- (Spanner and the emulator both reject it as "not fully partitionable"):
--
--   gcloud spanner databases execute-sql <database> --instance=<instance> --sql="<statement>"
--
-- Check the count first with `go run ./cmd/audit` (TEXT INTEGRITY, "law refs missing a
-- version"), and again after: it should be 0. Orphans come only from pruned versions, so there
-- are few; one transaction allows 80,000 mutations, and each deleted row costs about two (the
-- row and its idx_bill_law_refs_section entry).
--
-- Against the local emulator, first: export CLOUDSDK_API_ENDPOINT_OVERRIDES_SPANNER=http://localhost:9020/

DELETE FROM bill_law_refs r WHERE NOT EXISTS (SELECT 1 FROM bill_text_versions v WHERE v.bill_id = r.bill_id AND v.version_id = r.version_id);
