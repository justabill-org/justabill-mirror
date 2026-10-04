-- sync_retry queues every bill or GovInfo package that still failed after the upstream client's
-- retries, so the next run tries it again after the watermark has moved on
-- (docs/design/67-upstream-quota-retries.md, "sync_retry and the watermark").
-- step is "bills" or "govinfo"; item_id is a bill ID (hr-119-43) or a GovInfo package ID.
-- next_attempt_at is NULL once the item is given up (10 attempts or a permanent error), and
-- last_error is redacted and at most 1 KiB.
CREATE TABLE sync_retry (
  step STRING(64) NOT NULL,
  congress INT64 NOT NULL,
  item_id STRING(MAX) NOT NULL,
  attempts INT64 NOT NULL,
  first_failed_at TIMESTAMP NOT NULL,
  last_failed_at TIMESTAMP NOT NULL,
  next_attempt_at TIMESTAMP,
  last_error STRING(MAX),
) PRIMARY KEY (step, congress, item_id);
