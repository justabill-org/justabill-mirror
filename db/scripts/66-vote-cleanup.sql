-- Honest vote data (#66, design docs/design/66-honest-vote-data.md): cleanup for databases
-- written by the pipeline before the change. Production is loaded fresh and doesn't need it.
--
-- Each statement is idempotent. Run them one at a time as Partitioned DML (a normal
-- transaction can exceed Spanner's mutation limit on a full congress of member votes):
--
--   gcloud spanner databases execute-sql <database> --instance=<instance> \
--     --enable-partitioned-dml --sql="<statement>"
--
-- Against the local emulator, first: export CLOUDSDK_API_ENDPOINT_OVERRIDES_SPANNER=http://localhost:9020/

-- Step 1: canonical member vote values. The House records "Aye"/"No" on recorded votes;
-- the pipeline now stores Yea/Nay/Present/Not Voting (model.NormalizeMemberVote).
UPDATE member_votes SET vote = 'Yea' WHERE LOWER(TRIM(vote)) IN ('aye', 'yea') AND vote != 'Yea';
UPDATE member_votes SET vote = 'Nay' WHERE LOWER(TRIM(vote)) IN ('no', 'nay') AND vote != 'Nay';
UPDATE member_votes SET vote = 'Present' WHERE LOWER(TRIM(vote)) IN ('present', 'present, giving live pair') AND vote != 'Present';
UPDATE member_votes SET vote = 'Not Voting' WHERE LOWER(TRIM(vote)) = 'not voting' AND vote != 'Not Voting';

-- Step 2: old-format voice votes ({chamber}-{congress}-voice-{yyyymmdd}), one per chamber and
-- day with a synthetic Yea for every member. member_votes is interleaved ON DELETE CASCADE, so
-- the synthetic member votes go with them. The pipeline now writes one row per bill with no
-- member votes ({chamber}-{congress}-{voice|uc}-{bill_id}-{yyyymmdd}, #188), which this pattern
-- doesn't match; the next bill sync recreates them.
DELETE FROM congressional_votes WHERE REGEXP_CONTAINS(vote_id, r'^(house|senate)-\d+-voice-\d{8}$');

-- Step 3: re-fetch every roll call so Senate votes get their bill_id, House votes synced before
-- their bill get linked, and Senate vote dates are parsed instead of set to the sync time.
-- This isn't SQL. From pipeline/, with the Spanner flags or env vars the backfill task uses
-- (--steps members,votes skips the bill sync, so this costs only the member sync's Congress.gov
-- requests; the vote feeds themselves have no quota):
--
--   go run ./cmd/backfill --congress 119 --steps members,votes --force
--
-- Without --session, every session of the congress that has started is synced (#65).
