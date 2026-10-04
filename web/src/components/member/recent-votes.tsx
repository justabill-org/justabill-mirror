import Link from "next/link";
import type { MemberVoteSummary } from "@/lib/types";
import { billLabelFromId } from "@/lib/graph";
import { formatDate } from "@/lib/utils";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { VoteBadge } from "@/components/member/vote-badge";

/**
 * A member's recent roll calls (#787): the bill each was on (linking to it when we have the bill),
 * the question, result, chamber and date, and how the member voted.
 */
export function RecentVotes({ votes }: { votes: MemberVoteSummary[] }) {
  return (
    <section aria-label="Recent votes">
      <Card>
        <CardHeader>
          <CardTitle className="text-lg">Recent Votes</CardTitle>
        </CardHeader>
        <CardContent>
          {votes.length === 0 ? (
            <p className="text-sm text-muted-foreground">No recorded votes yet.</p>
          ) : (
            <ul className="-mt-3 divide-y divide-border">
              {votes.map((vote) => (
                <RecentVote key={vote.vote_id} vote={vote} />
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </section>
  );
}

function RecentVote({ vote }: { vote: MemberVoteSummary }) {
  // The question is the headline only when there's no bill; otherwise it's a detail.
  const details = [vote.bill_id ? vote.question : undefined, vote.result, vote.chamber, formatDate(vote.vote_date)];
  return (
    <li className="flex items-start justify-between gap-3 py-3 text-sm">
      <div className="min-w-0 flex-1">
        <p className="font-medium text-foreground [overflow-wrap:anywhere]">
          <VoteHeadline vote={vote} />
        </p>
        <p className="mt-0.5 text-muted-foreground">{details.filter(Boolean).join(" · ")}</p>
      </div>
      <span className="shrink-0">
        <span className="sr-only">Vote: </span>
        <VoteBadge vote={vote.member_vote} />
      </span>
    </li>
  );
}

/**
 * "H.R. 808 · Title" linking to the bill; the label alone, as text, when the bill isn't loaded (a
 * link would be a 404, design #66); the question for a vote on no bill, or "Roll call vote".
 */
function VoteHeadline({ vote }: { vote: MemberVoteSummary }) {
  if (!vote.bill_id) return <>{vote.question || "Roll call vote"}</>;
  const label = billLabelFromId(vote.bill_id);
  if (!vote.bill_title) return <>{label}</>;
  return (
    <Link href={`/bills/${vote.bill_id}`} className="transition-colors hover:text-link hover:underline">
      {label} · {vote.bill_title}
    </Link>
  );
}
