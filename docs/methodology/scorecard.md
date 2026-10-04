# How the scorecard works

When you vote on a bill, we compare your vote with how each of your representatives voted on
that bill. We use only the final recorded vote each chamber took on the bill itself: passage,
agreeing to a resolution, a conference report, accepting the other chamber's changes, or
overriding a veto. We don't count procedural votes (such as cloture, motions to proceed, table,
recommit or discharge) or votes on amendments, because they're often about scheduling or
strategy, not the bill. If a chamber voted on a bill more than once, we use the most recent of
those final votes.

Bills passed by voice vote have no record of how each member voted, so we can't compare them.
If your representative didn't vote or voted "Present", we show that, but it doesn't count for or
against them. The alignment percentage is the share of compared bills where you and your
representative voted the same way. Bills you skipped are never counted.

Your representatives are the members who hold your House seat and your state's Senate seats
now, the people on your ballot or answering to you today, not whoever held those seats when a
vote was taken. You can compare them on the current Congress, the previous one, or both. A
senator who served in the House in the previous Congress is compared on those House votes too,
and each vote says which chamber it was cast in.

Vote records come from the House Clerk and the Senate's roll-call files. The rule is named
`final-passage-v1`, and its code is open source ([`db/scoring`](../../db/scoring)). If you think
a vote is classified wrongly, tell us through the
[report-a-problem link](https://github.com/justabill-org/justabill-mirror/issues/new).

---

*This text was approved with the scorecard design (design 69, kept in the project's private
working repository, like the other design docs). The site's
[Methodology page](../../web/src/app/%28public%29/%28trust%29/methodology/page.tsx) summarizes it and
links here. The rule's exact question forms are in [`rule.go`](../../db/scoring/rule.go), and
[`testdata/questions.tsv`](../../db/scoring/testdata/questions.tsv) lists the real House and Senate
questions it's tested against. A change to what counts means a new rule name (`final-passage-v2`)
and an update to this page.*
