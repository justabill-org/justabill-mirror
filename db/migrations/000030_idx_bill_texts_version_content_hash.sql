-- The text queues test a text's content_hash without its content (docs/design/746-spanner-review.md,
-- story 2): storing it in idx_bill_texts_version lets the summary and law-change queues read the
-- index alone instead of each multi-megabyte bill_texts row. Spanner fills it from the rows already
-- there, online; the code already running doesn't name it, so it keeps working.
ALTER INDEX idx_bill_texts_version ADD STORED COLUMN content_hash;
