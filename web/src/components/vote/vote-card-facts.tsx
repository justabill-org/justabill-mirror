import Link from "next/link";
import type { Bill, BillCardFacts, BillSummary } from "@/lib/types";
import { BILL_STATUS_LABELS } from "@/lib/types";
import { PartyIndicator } from "@/components/member/party-indicator";
import { formatDate } from "@/lib/utils";
import { cardLead, chamberName, lawName, passageFailed, passageShort, whoItAffects } from "@/lib/vote-card";

// What a /vote card says about its bill (#662): the lead summary, who it affects and the fact rows.
// The Details popup (vote-details.tsx, #717) has everything at full length.

type Card = BillCardFacts | null | undefined;
type Summary = BillSummary | null | undefined;

/** "Sen. McConnell, Mitch" and "R-KY" from Congress.gov's "Sen. McConnell, Mitch [R-KY]". */
function sponsorParts(bill: Bill): { name: string; tag: string; party: string } | null {
  const s = bill.sponsors?.[0];
  if (!s) return null;
  return { name: s.fullName.split("[")[0].trim(), tag: `${s.party}-${s.state}`, party: s.party };
}

/** The card's lead: what the bill does, from the AI summary or else the CRS summary, labeled with its source. */
export function CardLead({ summary, card }: { summary: Summary; card: Card }) {
  const lead = cardLead(summary, card);
  if (!lead) {
    return (
      <p className="text-sm text-muted-foreground">
        No summary of this bill yet. Details links to its official record.
      </p>
    );
  }
  return (
    <div className="space-y-1">
      {lead.source === "ai" ? (
        <p className="text-xs text-muted-foreground">
          <span className="font-medium text-foreground">AI summary</span>, not reviewed by a person; may contain
          errors.{" "}
          <Link href="/methodology#summaries" className="underline underline-offset-2 hover:text-foreground">
            How it&apos;s made
          </Link>
        </p>
      ) : (
        <p className="text-xs text-muted-foreground">
          <span className="font-medium text-foreground">Official summary</span> · Congressional Research Service
        </p>
      )}
      <p className="text-sm leading-relaxed text-foreground line-clamp-5">{lead.text}</p>
    </div>
  );
}

/** "Who it affects", from the AI summary only. */
export function CardWhoItAffects({ summary }: { summary: Summary }) {
  const who = whoItAffects(summary);
  if (!who) return null;
  return (
    <p className="text-sm leading-relaxed text-foreground line-clamp-2">
      <span className="font-semibold">Who it affects:</span> {who}
    </p>
  );
}

/**
 * The fact rows: how each chamber voted, when it became law (or its status, when it hasn't), and
 * the sponsor. The votes row says "Passed" only when no chamber's final vote failed.
 */
export function CardFactRows({ bill, card }: { bill: Bill; card: Card }) {
  const sponsor = sponsorParts(bill);
  const passage = card?.passage ?? [];
  const enacted = card?.enacted;
  const law = lawName(enacted);
  const status = bill.current_status ? BILL_STATUS_LABELS[bill.current_status] : null;
  return (
    <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm">
      {passage.length > 0 && (
        <>
          <dt className="text-muted-foreground">{passage.some(passageFailed) ? "Final votes" : "Passed"}</dt>
          <dd className="text-foreground">
            {passage.map((p, i) => (
              <span key={p.chamber}>
                {i > 0 && " · "}
                <span className="whitespace-nowrap">
                  {chamberName(p.chamber)}: <span className="font-medium">{passageShort(p)}</span>
                </span>
              </span>
            ))}
          </dd>
        </>
      )}
      {enacted ? (
        <>
          <dt className="text-muted-foreground">Became law</dt>
          <dd className="text-foreground">
            <span className="whitespace-nowrap">{formatDate(enacted.date)}</span>
            {law && (
              <>
                <span aria-hidden="true"> · </span>
                <span className="whitespace-nowrap font-medium">{law}</span>
              </>
            )}
          </dd>
        </>
      ) : (
        status && (
          <>
            <dt className="text-muted-foreground">Status</dt>
            <dd className="text-foreground">
              {status}
              {bill.status_date && (
                <span className="whitespace-nowrap text-muted-foreground"> · {formatDate(bill.status_date)}</span>
              )}
            </dd>
          </>
        )
      )}
      {sponsor && (
        <>
          <dt className="text-muted-foreground">Sponsor</dt>
          <dd className="flex min-w-0 items-center gap-1.5 text-foreground">
            <PartyIndicator party={sponsor.party} size="sm" />
            <span className="truncate">{sponsor.name}</span>
            <span className="shrink-0 text-muted-foreground">({sponsor.tag})</span>
          </dd>
        </>
      )}
    </dl>
  );
}
