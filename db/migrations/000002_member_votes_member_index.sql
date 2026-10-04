-- Reads one member's roll-call positions without scanning every vote
-- (VoteRepo.MemberPositions; docs/design/72-account-free-voting.md, docs/design/69-scorecard-methodology.md).
-- member_votes is interleaved in congressional_votes, so its primary key can't serve a
-- member_id lookup. STORING (vote) lets the positions query read the vote from the index.
CREATE INDEX idx_member_votes_member ON member_votes(member_id) STORING (vote);
