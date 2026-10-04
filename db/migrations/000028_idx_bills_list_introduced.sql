-- The bill list's default order, newest introduced first, within a congress (#760,
-- docs/design/746-spanner-review.md). It stores every column the list filters on, so a page and
-- its COUNT(*) are read from the index alone, stopping at offset + limit, rather than reading and
-- sorting the whole congress per request. A new list filter column must be added to STORING here
-- and in idx_bills_list_latest_action.
CREATE INDEX idx_bills_list_introduced
  ON bills(congress, introduced_date DESC, bill_id)
  STORING (bill_type, current_status, origin_chamber, policy_area_id);
