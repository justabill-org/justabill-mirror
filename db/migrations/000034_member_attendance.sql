-- Each member's roll-call attendance in a congress and chamber, computed by the vote sync
-- (docs/design/agent-1742-member-attendance.md, rule attendance-v1). eligible counts the roll
-- calls the member was listed on, quorum calls excluded; missed and present count those answered
-- Not Voting and Present. chamber_members and chamber_median_pct (the median of missed / eligible
-- × 100 over the chamber's members with a row) repeat on every row of a chamber and congress, so
-- one row serves a member page. Each run replaces a congress's rows.
CREATE TABLE member_attendance (
  member_id STRING(36) NOT NULL,
  congress INT64 NOT NULL,
  chamber STRING(MAX) NOT NULL,
  eligible INT64 NOT NULL,
  missed INT64 NOT NULL,
  present INT64 NOT NULL,
  chamber_members INT64 NOT NULL,
  chamber_median_pct FLOAT64 NOT NULL,
  computed_at TIMESTAMP NOT NULL OPTIONS (allow_commit_timestamp = true),
  CONSTRAINT fk_member_attendance_member FOREIGN KEY(member_id) REFERENCES members(bioguide_id),
  CONSTRAINT fk_member_attendance_congress FOREIGN KEY(congress) REFERENCES congresses(number),
) PRIMARY KEY(member_id, congress, chamber);

GRANT SELECT ON TABLE member_attendance TO ROLE api;
