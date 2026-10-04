-- The refetch queue (QueryUnfetchedTextVersions) tests a text's fetched_at without its content
-- (docs/design/746-spanner-review.md, story 2): storing it in idx_bill_texts_version lets the queue
-- read the index alone. Filled online from the rows already there; the code already running doesn't
-- name it.
ALTER INDEX idx_bill_texts_version ADD STORED COLUMN fetched_at;
