-- Indexes no query or graph traversal uses (docs/design/746-spanner-review.md, "Unused and
-- duplicate indexes"): no query filters gao_reports by report_number, every diff read carries
-- bill_id, and nothing reads bill_committees by committee (fk_bill_committees_committee keeps its
-- own backing index). civic_graph's edges don't name an index, so it's unchanged. IF EXISTS keeps
-- the file safe to run again; the deploy PR flags it for the maintainer, as every DROP is (#87).
DROP INDEX IF EXISTS idx_gao_reports_number;
DROP INDEX IF EXISTS idx_btd_id;
DROP INDEX IF EXISTS idx_bill_committees_committee;
