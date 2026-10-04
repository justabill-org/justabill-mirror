-- The bill list's sort=latest_action, newest first, within a congress (#760): the bills page's
-- sort. The same STORING columns as idx_bills_list_introduced, for the same reason.
CREATE INDEX idx_bills_list_latest_action
  ON bills(congress, latest_action_date DESC, bill_id)
  STORING (bill_type, current_status, origin_chamber, policy_area_id);
