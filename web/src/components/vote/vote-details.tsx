"use client";

import { useEffect, useId, useState } from "react";
import Link from "next/link";
import { getBill, getBillLawChanges } from "@/lib/api";
import { crsParagraphs, crsSummaryUrl } from "@/lib/crs";
import { LAW_CHANGE_KIND_LABELS, lawCitation } from "@/lib/law-changes";
import { reportError } from "@/lib/obs/browser";
import { congressGovUrl } from "@/lib/seo";
import { billSponsors } from "@/lib/sponsors";
import { congressGovTextUrl } from "@/lib/trust";
import type {
  Bill,
  BillCardFacts,
  BillDetailResponse,
  BillLawChangesResponse,
  BillSummary,
  CardPassage,
  UserVoteChoice,
} from "@/lib/types";
import { BILL_STATUS_LABELS, BILL_TYPE_LABELS } from "@/lib/types";
import { formatDate } from "@/lib/utils";
import {
  MAX_LAW_CHANGES,
  chamberName,
  cosponsorParties,
  lawName,
  partyCountLabel,
  passageFailed,
  whoItAffects,
} from "@/lib/vote-card";
import { SummaryProvenance } from "@/components/bill/summary-provenance";
import { PartyIndicator } from "@/components/member/party-indicator";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetClose, SheetContent, SheetTitle } from "@/components/ui/sheet";

// /vote's Details popup (#717): what the card has no room for, in the order a voter asks it. What
// the bill does and who it affects, what it changes in law, how Congress voted, who backed it and
// where it stands. The card's own facts aren't repeated, and the vote buttons stay at the bottom
// so nobody has to close the popup to vote.
//
// Left out on purpose: how the reader's representatives voted and how other users voted. Both come
// after the reader's own vote (the scorecard and the bill page), so they can't steer it.
//
// How Congress voted and where the bill stands come with the card (`include=card`, #705), so they
// show at once; the full CRS summary, the cosponsors and the law changes are read when it opens.

type Card = BillCardFacts | null | undefined;

interface More {
  detail: BillDetailResponse;
  changes: BillLawChangesResponse | null;
}

type MoreState = { status: "loading" } | { status: "error" } | ({ status: "loaded" } & More);

/** Whether this build may show the example bill when the API can't be reached: `next dev` and Vercel previews (#678). */
function examplesAllowed(): boolean {
  return process.env.NODE_ENV === "development" || process.env.NEXT_PUBLIC_VERCEL_ENV === "preview";
}

async function loadMore(billId: string, card: Card): Promise<More> {
  // The law changes are optional (the rest still shows when only they fail), and skipped when the
  // card says the bill changes no law.
  const needChanges = !card || card.law_change_count > 0;
  try {
    const [detail, changes] = await Promise.all([
      getBill(billId),
      needChanges ? getBillLawChanges(billId).catch(() => null) : Promise.resolve(null),
    ]);
    return { detail, changes };
  } catch (err) {
    // The example bill's own record only: another bill never gets its facts.
    if (!examplesAllowed()) throw err;
    const { billDetail } = await import("@/lib/examples");
    if (billDetail.bill.id !== billId) throw err;
    return { detail: billDetail, changes: null };
  }
}

/**
 * The rest of a bill's record, read from the browser the first time the sheet opens, so /vote
 * stays one cached page and only a reader who asks pays for the two calls.
 */
function useMore(billId: string, card: Card, open: boolean): [MoreState, () => void] {
  const [result, setResult] = useState<{ attempt: number; state: MoreState } | null>(null);
  const [attempt, setAttempt] = useState(0);
  const settled = result?.attempt === attempt ? result.state : null;
  const done = settled?.status === "loaded";

  useEffect(() => {
    if (!open || done) return;
    let live = true;
    loadMore(billId, card).then(
      (more) => live && setResult({ attempt, state: { status: "loaded", ...more } }),
      (err: unknown) => {
        reportError(err);
        if (live) setResult({ attempt, state: { status: "error" } });
      },
    );
    return () => {
      live = false;
    };
  }, [billId, card, open, attempt, done]);

  return [settled ?? { status: "loading" }, () => setAttempt((n) => n + 1)];
}

interface VoteDetailsProps {
  bill: Bill;
  /** The AI summary the deck already has, shown at once. */
  summary?: BillSummary | null;
  /** The card's facts from the list: the CRS lead, each chamber's final vote and the law, if any. */
  card?: Card;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Votes from the sheet: it closes and the card leaves as it does for the card's own buttons. */
  onVote: (vote: UserVoteChoice) => void;
}

export function VoteDetails({ bill, summary, card, open, onOpenChange, onVote }: VoteDetailsProps) {
  const [more, retry] = useMore(bill.id, card, open);
  const typeLabel = BILL_TYPE_LABELS[bill.bill_type] || bill.bill_type.toUpperCase();
  const vote = (choice: UserVoteChoice) => {
    onOpenChange(false);
    onVote(choice);
  };

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      {/* A popup in the middle of the screen (#717), sized in dvh so a phone's browser bar can't
          push the vote buttons off it. */}
      <SheetContent side="center" className="flex max-w-2xl flex-col overflow-hidden">
        <div className="flex w-full shrink-0 items-center justify-between gap-3 px-4 pt-2 sm:px-6">
          <p className="flex min-w-0 items-center gap-2">
            <span className="whitespace-nowrap text-sm font-semibold tabular-nums text-foreground">
              {typeLabel} {bill.number}
            </span>
            {bill.policy_area && (
              <Badge variant="secondary" className="truncate text-xs">
                {bill.policy_area}
              </Badge>
            )}
          </p>
          <SheetClose onClick={() => onOpenChange(false)} className="static -mr-2 flex h-11 w-11 shrink-0 items-center justify-center" />
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain">
          <div className="space-y-6 px-4 pb-6 pt-1 sm:px-6">
            <SheetTitle className="text-left text-lg leading-snug">{bill.title}</SheetTitle>
            <WhatItDoes bill={bill} summary={summary} card={card} more={more} />
            {more.status === "loading" && <MoreLoading />}
            {more.status === "error" && <MoreFailed onRetry={retry} />}
            {more.status === "loaded" && <LawChanges card={card} changes={more.changes} />}
            <HowCongressVoted card={card} />
            {more.status === "loaded" && <WhoBackedIt detail={more.detail} />}
            <WhereItStands bill={bill} card={card} />
            <Links bill={bill} />
          </div>
        </div>

        <div className="shrink-0 border-t border-border bg-background">
          <div className="flex items-center gap-2 px-4 py-3 sm:px-6">
            <Button
              size="lg"
              variant="outline"
              className="flex-1 border-vote-nay/50 text-vote-nay hover:border-vote-nay hover:bg-vote-nay hover:text-vote-nay-foreground"
              onClick={() => vote("nay")}
            >
              Nay
            </Button>
            <Button
              size="lg"
              variant="outline"
              className="flex-1 border-vote-skip/50 text-vote-skip hover:border-vote-skip hover:bg-vote-skip hover:text-vote-skip-foreground"
              onClick={() => vote("skip")}
            >
              Skip
            </Button>
            <Button
              size="lg"
              variant="outline"
              className="flex-1 border-vote-yea/50 text-vote-yea hover:border-vote-yea hover:bg-vote-yea hover:text-vote-yea-foreground"
              onClick={() => vote("yea")}
            >
              Yea
            </Button>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  const id = useId();
  return (
    <section aria-labelledby={id} className="space-y-2">
      <h3 id={id} className="text-sm font-semibold text-foreground">
        {title}
      </h3>
      {children}
    </section>
  );
}

/**
 * What the bill does: the AI summary in full when there is one, with the official CRS summary
 * folded under it; the CRS summary alone otherwise; and a plain "none yet" once we know there's
 * neither. Who it affects follows as its own section.
 */
function WhatItDoes({ bill, summary, card, more }: { bill: Bill; summary?: BillSummary | null; card: Card; more: MoreState }) {
  const ai = summary?.short_summary ? summary : more.status === "loaded" ? more.detail.summary : null;
  // The full CRS summary once it's read; until then (or if that read failed) the card's lead.
  const full = more.status === "loaded" ? (more.detail.crs_summary ?? null) : null;
  const crs = full ?? (more.status === "loaded" ? null : (card?.crs ?? null));
  const paragraphs = full ? crsParagraphs(full.text) : crs && "lead" in crs ? [crs.lead] : [];
  const who = whoItAffects(ai);
  const crsBody = crs && (
    <div className="space-y-2">
      <p className="text-xs text-muted-foreground">
        Congressional Research Service, of the bill as <em>{crs.action_desc}</em> ·{" "}
        {formatDate(crs.action_date, "long")}
      </p>
      {paragraphs.map((p, i) => (
        <p key={i} className="text-sm leading-relaxed text-foreground">
          {p}
        </p>
      ))}
      <a
        href={crsSummaryUrl(bill, crs)}
        target="_blank"
        rel="noopener noreferrer"
        className="inline-block text-xs text-muted-foreground underline underline-offset-2 hover:text-foreground"
      >
        Read this summary on Congress.gov
      </a>
    </div>
  );

  if (ai?.short_summary) {
    return (
      <>
        <Section title="AI summary">
          <p className="text-xs text-muted-foreground">
            Written by AI from the bill&apos;s official text and not reviewed by a person. It may contain errors.{" "}
            <Link href="/methodology#summaries" className="underline underline-offset-2 hover:text-foreground">
              How it&apos;s made
            </Link>
          </p>
          <p className="leading-relaxed text-foreground">{ai.short_summary}</p>
          {ai.long_summary && (
            <p className="whitespace-pre-wrap text-sm leading-relaxed text-muted-foreground">{ai.long_summary}</p>
          )}
          <SummaryProvenance summary={ai} />
        </Section>
        {who && (
          <Section title="Who it affects">
            <p className="text-sm leading-relaxed text-foreground">{who}</p>
          </Section>
        )}
        {crsBody && (
          <details className="group rounded-lg border border-border">
            <summary className="flex min-h-11 cursor-pointer list-none items-center justify-between gap-3 px-3 text-sm font-medium text-foreground [&::-webkit-details-marker]:hidden">
              Official summary
              <span aria-hidden="true" className="text-muted-foreground transition-transform group-open:rotate-180">
                ▾
              </span>
            </summary>
            <div className="px-3 pb-3">{crsBody}</div>
          </details>
        )}
      </>
    );
  }
  if (crsBody) return <Section title="Official summary">{crsBody}</Section>;
  if (more.status !== "loaded") return null;
  const text = congressGovTextUrl(bill);
  return (
    <Section title="Summary">
      <p className="text-sm text-muted-foreground">
        This bill has no summary yet.{" "}
        {text && (
          <a href={text} target="_blank" rel="noopener noreferrer" className="underline underline-offset-2 hover:text-foreground">
            Read its text on Congress.gov
          </a>
        )}
      </p>
    </Section>
  );
}

function MoreLoading() {
  return (
    <div role="status" aria-label="Loading more about this bill" className="space-y-3">
      <div className="h-4 w-40 animate-pulse rounded bg-muted" />
      <div className="h-16 w-full animate-pulse rounded-lg bg-muted" />
      <div className="h-4 w-32 animate-pulse rounded bg-muted" />
      <div className="h-10 w-full animate-pulse rounded-lg bg-muted" />
    </div>
  );
}

function MoreFailed({ onRetry }: { onRetry: () => void }) {
  return (
    <div role="alert" className="flex flex-wrap items-center gap-3 text-sm text-muted-foreground">
      <p>The rest of this bill&apos;s record couldn&apos;t be loaded right now.</p>
      <Button variant="outline" size="sm" onClick={onRetry}>
        Try again
      </Button>
    </div>
  );
}

/** The first sections of law the bill changes, with how many more the bill page has. */
function LawChanges({ card, changes }: { card: Card; changes: BillLawChangesResponse | null }) {
  const lawChanges = changes?.changes ?? [];
  if (lawChanges.length === 0) return null;
  const total = Math.max(card?.law_change_count ?? 0, lawChanges.length);
  const shown = lawChanges.slice(0, MAX_LAW_CHANGES);
  return (
    <Section title="What it changes in law">
      {changes?.ai_generated && (
        <p className="text-xs text-muted-foreground">
          The explanations are written by AI from the bill text and the current US Code, and not reviewed by a
          person.
        </p>
      )}
      <ul className="space-y-2">
        {shown.map((c) => (
          <li key={lawCitation(c)} className="rounded-lg border border-border p-3 text-sm">
            <p className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <Badge variant="outline" className="text-xs">
                {LAW_CHANGE_KIND_LABELS[c.change_kind]}
              </Badge>
              <span className="font-medium text-foreground">{lawCitation(c)}</span>
              {c.heading && <span className="text-muted-foreground">{c.heading}</span>}
            </p>
            {c.explanation && <p className="mt-1 line-clamp-3 text-muted-foreground">{c.explanation}</p>}
          </li>
        ))}
      </ul>
      {total > shown.length && (
        <p className="text-xs text-muted-foreground">And {total - shown.length} more on the bill page.</p>
      )}
    </Section>
  );
}

/** Each chamber's final vote, from the card (the API picks them with db/scoring's allowlist). */
function HowCongressVoted({ card }: { card: Card }) {
  const passage = card?.passage ?? [];
  if (passage.length === 0) return null;
  return (
    <Section title="How Congress voted">
      <ul className="space-y-2">
        {passage.map((v) => (
          <PassageItem key={v.chamber} vote={v} />
        ))}
      </ul>
    </Section>
  );
}

/** The sponsor, and the cosponsors counted by party. */
function WhoBackedIt({ detail }: { detail: BillDetailResponse }) {
  const { sponsor, cosponsors } = billSponsors(detail);
  if (!sponsor) return null;
  const parties = cosponsorParties(cosponsors);
  return (
    <Section title="Who backed it">
      <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-foreground">
        <PartyIndicator party={sponsor.party ?? ""} size="sm" />
        <span>
          Sponsored by {sponsor.name}
          {sponsor.party && sponsor.state && ` (${sponsor.party}-${sponsor.state})`}
        </span>
      </p>
      <p className="text-sm text-muted-foreground">
        {cosponsors.length === 0
          ? "No cosponsors."
          : `${cosponsors.length} ${cosponsors.length === 1 ? "cosponsor" : "cosponsors"}` +
            (parties.length > 0 ? `: ${parties.map(partyCountLabel).join(", ")}.` : ".")}
      </p>
    </Section>
  );
}

/** When it became law and its number, from the card; or its status. */
function WhereItStands({ bill, card }: { bill: Bill; card: Card }) {
  // Without card facts (an example bill), a law still reads "Became law <date>".
  const enacted =
    card?.enacted ?? (bill.current_status === "became_law" && bill.status_date ? { date: bill.status_date } : null);
  const law = lawName(enacted);
  const status = bill.current_status ? BILL_STATUS_LABELS[bill.current_status] : null;
  return (
    <Section title="Where it stands">
      <p className="text-sm text-foreground">
        {enacted ? (
          <>
            Became law {formatDate(enacted.date)}
            {law && <span className="text-muted-foreground"> · {law}</span>}
          </>
        ) : (
          <>
            {status ?? "Status not recorded"}
            {bill.status_date && <span className="text-muted-foreground"> · since {formatDate(bill.status_date)}</span>}
          </>
        )}
      </p>
      {bill.introduced_date && (
        <p className="text-sm text-muted-foreground">Introduced {formatDate(bill.introduced_date)}</p>
      )}
    </Section>
  );
}

function PassageItem({ vote }: { vote: CardPassage }) {
  const when = formatDate(vote.date);
  const chamber = chamberName(vote.chamber);
  if (vote.method !== "roll") {
    const how = vote.method === "voice" ? "by voice vote" : "by unanimous consent";
    return (
      <li className="rounded-lg border border-border p-3 text-sm">
        <p className="font-medium text-foreground">
          {chamber}: {passageFailed(vote) ? "failed" : "passed"} {how}{" "}
          <span className="font-normal text-muted-foreground">· {when}</span>
        </p>
        <p className="mt-1 text-muted-foreground">No individual votes were recorded.</p>
      </li>
    );
  }
  const tally = vote.yeas != null && vote.nays != null ? `, ${vote.yeas}–${vote.nays}` : "";
  return (
    <li className="rounded-lg border border-border p-3 text-sm">
      <p className="font-medium text-foreground">
        {chamber}: {vote.result ?? "Roll call"}
        {tally} <span className="font-normal text-muted-foreground">· {when}</span>
      </p>
      <p className="mt-1 text-muted-foreground">
        {vote.question}
        {vote.roll_number != null && <> · Roll call {vote.roll_number}</>}
        {vote.not_voting != null && vote.not_voting > 0 && <> · {vote.not_voting} not voting</>}
        {vote.present != null && vote.present > 0 && <> · {vote.present} present</>}
      </p>
    </li>
  );
}

function Links({ bill }: { bill: Bill }) {
  const official = congressGovUrl(bill);
  return (
    <p className="flex flex-wrap gap-x-4 gap-y-2 text-sm">
      <Link href={`/bills/${bill.id}`} className="font-medium text-link underline underline-offset-2">
        Open the bill page: full text and every action
      </Link>
      {official && (
        <a
          href={official}
          target="_blank"
          rel="noopener noreferrer"
          className="text-muted-foreground underline underline-offset-2 hover:text-foreground"
        >
          This bill on Congress.gov
        </a>
      )}
    </p>
  );
}
