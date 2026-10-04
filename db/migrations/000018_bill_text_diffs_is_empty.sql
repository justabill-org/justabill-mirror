-- is_empty marks a version pair that was diffed and had no section added, removed or modified
-- (docs/design/401-remember-empty-diffs.md). The row lets the diff sweep remember the pair
-- instead of re-diffing it every run. Every reader of bill_text_diffs filters NOT is_empty, so
-- an empty diff never reaches the web or the diff-summary queue.
ALTER TABLE bill_text_diffs ADD COLUMN is_empty BOOL NOT NULL DEFAULT (false);
