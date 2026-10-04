-- The disapproved rule an AI summary was written with (docs/design/590-cra-disapproved-rules.md,
-- "Prompt context"): the context_hash of the bill's bill_cra_rules row when the prompt carried its
-- <disapproved_rule> block, or NULL when it carried none. A CRA resolution is due again when its
-- row's context_hash differs. Additive and nullable, so the code already running keeps working.
ALTER TABLE bill_summaries ADD COLUMN source_rule_hash STRING(64);
