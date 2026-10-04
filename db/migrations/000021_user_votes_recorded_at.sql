-- When the server stored a vote (#458). voted_at is when the user says they voted: an import keeps
-- the browser's time, which can be any time in the past. The daily vote cap and the aggregate
-- snapshot's burst and new-since-publish windows (docs/design/89-aggregate-analytics.md) read this
-- column instead, so an import can't slip past them. Every write sets it to the server's time.
-- NULL on rows stored before it; readers fall back to voted_at (COALESCE(recorded_at, voted_at)).
ALTER TABLE user_votes ADD COLUMN recorded_at TIMESTAMP;
