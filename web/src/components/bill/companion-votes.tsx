import Link from "next/link";
import type { CompanionRollCall } from "@/lib/types";
import { billLabelFromId, tallyByParty, UNKNOWN_PARTY } from "@/lib/graph";
import { Badge } from "@/components/ui/badge";
import { CollapsibleCard } from "@/components/ui/collapsible-card";
import { PartyIndicator } from "@/components/member/party-indicator";
import { formatDate } from "@/lib/utils";

interface CompanionVotesProps {
  rollCalls: CompanionRollCall[];
}

/**
 * How the other chamber voted on this bill's identical companion. Hidden when there are no
 * recorded roll calls (or the endpoint failed and the page passed an empty list).
 */
export function CompanionVotes({ rollCalls }: CompanionVotesProps) {
  if (rollCalls.length === 0) return null;

  return (
    <section className="mt-8" aria-label="Companion bill votes">
      <CollapsibleCard
        title="How the other chamber voted"
        summary={rollCalls.length === 1 ? "1 recorded vote" : `${rollCalls.length} recorded votes`}
        contentClassName="space-y-4"
      >
          <p className="text-sm text-muted-foreground">
            Recorded votes on this bill&apos;s identical companion in the other chamber.
          </p>
          <ul className="space-y-4">
            {rollCalls.map((rollCall) => (
              <li key={rollCall.vote_id} className="rounded-lg border border-border p-4">
                <CompanionRollCallItem rollCall={rollCall} />
              </li>
            ))}
          </ul>
      </CollapsibleCard>
    </section>
  );
}

function CompanionRollCallItem({ rollCall }: { rollCall: CompanionRollCall }) {
  const tallies = tallyByParty(rollCall.votes);
  const showOther = tallies.some((t) => t.other > 0);
  const total = tallies.reduce(
    (sum, t) => ({
      yea: sum.yea + t.yea,
      nay: sum.nay + t.nay,
      present: sum.present + t.present,
      notVoting: sum.notVoting + t.notVoting,
      other: sum.other + t.other,
    }),
    { yea: 0, nay: 0, present: 0, notVoting: 0, other: 0 }
  );
  const date = formatDate(rollCall.vote_date, "long");
  const members = [...rollCall.votes].sort(
    (a, b) => a.last_name.localeCompare(b.last_name) || a.first_name.localeCompare(b.first_name)
  );

  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-baseline gap-2">
          <Link
            href={`/bills/${rollCall.companion_bill_id}`}
            className="text-sm font-semibold tabular-nums text-foreground underline underline-offset-2"
          >
            {billLabelFromId(rollCall.companion_bill_id)}
          </Link>
          <span className="text-sm text-muted-foreground">
            {rollCall.chamber} &middot; {date}
          </span>
        </div>
        {rollCall.result && <Badge variant="outline">{rollCall.result}</Badge>}
      </div>
      {rollCall.question && <p className="mt-1 text-sm text-foreground">{rollCall.question}</p>}

      <table className="mt-3 w-full text-sm">
        <caption className="sr-only">
          {rollCall.chamber} vote on {billLabelFromId(rollCall.companion_bill_id)} by party
        </caption>
        <thead>
          <tr className="text-left text-xs text-muted-foreground">
            <th scope="col" className="py-1 font-medium">Party</th>
            <th scope="col" className="py-1 text-right font-medium">Yea</th>
            <th scope="col" className="py-1 text-right font-medium">Nay</th>
            <th scope="col" className="py-1 text-right font-medium">Present</th>
            <th scope="col" className="py-1 text-right font-medium">Not Voting</th>
            {showOther && <th scope="col" className="py-1 text-right font-medium">Other</th>}
          </tr>
        </thead>
        <tbody>
          {tallies.map((t) => (
            <tr key={t.party} className="border-t border-border">
              <th scope="row" className="py-1 text-left font-normal">
                {t.party === UNKNOWN_PARTY ? (
                  <span className="text-muted-foreground">Party unknown</span>
                ) : (
                  <PartyIndicator party={t.party} size="sm" showLabel />
                )}
              </th>
              <td className="py-1 text-right tabular-nums">{t.yea}</td>
              <td className="py-1 text-right tabular-nums">{t.nay}</td>
              <td className="py-1 text-right tabular-nums">{t.present}</td>
              <td className="py-1 text-right tabular-nums">{t.notVoting}</td>
              {showOther && <td className="py-1 text-right tabular-nums">{t.other}</td>}
            </tr>
          ))}
          <tr className="border-t border-border font-medium">
            <th scope="row" className="py-1 text-left">Total</th>
            <td className="py-1 text-right tabular-nums">{total.yea}</td>
            <td className="py-1 text-right tabular-nums">{total.nay}</td>
            <td className="py-1 text-right tabular-nums">{total.present}</td>
            <td className="py-1 text-right tabular-nums">{total.notVoting}</td>
            {showOther && <td className="py-1 text-right tabular-nums">{total.other}</td>}
          </tr>
        </tbody>
      </table>

      <details className="mt-3">
        <summary className="cursor-pointer text-sm font-medium text-foreground hover:underline">
          How each member voted ({members.length})
        </summary>
        <ul className="mt-2 grid gap-x-4 gap-y-1 text-sm sm:grid-cols-2">
          {members.map((m) => (
            <li key={m.member_id} className="flex items-center justify-between gap-2">
              <Link href={`/members/${m.member_id}`} className="flex items-center gap-1.5 hover:underline">
                {m.party && <PartyIndicator party={m.party} size="sm" />}
                <span>
                  {m.first_name} {m.last_name}
                  {m.party && <span className="text-muted-foreground"> ({m.party})</span>}
                </span>
              </Link>
              <span className="text-muted-foreground">{m.vote}</span>
            </li>
          ))}
        </ul>
      </details>
    </>
  );
}
