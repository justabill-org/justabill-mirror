-- The laws a bill became, as Congress.gov's bill endpoint lists them (#709): a JSON array of
-- {"type": "Public Law" or "Private Law", "number": "119-95"}. NULL for a bill that isn't law,
-- and for rows the pipeline hasn't synced since this column was added.
ALTER TABLE bills ADD COLUMN laws JSON;
