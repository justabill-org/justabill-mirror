-- How a bill passed (#1090), the order the vote deck deals bills in: 3 a contested passage roll
-- call (nays at least 10% of yeas plus nays, in either chamber), 2 other passage roll calls, 1 a
-- voice vote or unanimous consent (model.PassageRank). NULL is no passage vote, which sorts last
-- in descending order. The vote sync rewrites a congress's ranks from congressional_votes on
-- every run, so existing bills get theirs from the first run. Nullable: no row is rewritten here.
ALTER TABLE bills ADD COLUMN passage_rank INT64;
