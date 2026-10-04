-- The bill list by current status within a congress (#868): the /bills and /vote views (Laws,
-- Passed a chamber, In committee) filter on current_status, which idx_bills_list_* only store, so a
-- view's COUNT(*) read every bill of the congress and a rare status's page read far down the
-- index. Keyed on the status, a view's count and its page in latest-action order seek straight to
-- its bills; GET /bills/counts groups the congress's rows by the key. STORING holds every other
-- list filter column (a new one must be added here too, as in idx_bills_list_*) and updated_at,
-- the home page's sort for recent laws. The code already running doesn't name it.
CREATE INDEX idx_bills_list_status
  ON bills(congress, current_status, latest_action_date DESC, bill_id)
  STORING (bill_type, origin_chamber, policy_area_id, updated_at);
