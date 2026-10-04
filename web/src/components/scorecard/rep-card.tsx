"use client";

import Link from "next/link";
import { useId, useState, useSyncExternalStore } from "react";
import type { RepAlignment, VoteComparison } from "@/lib/types";
import { AGGREGATES_LABEL, METHODOLOGY_AGGREGATES, scopeName } from "@/lib/aggregates";
import type { LocalRep } from "@/lib/local/reps";
import type { MemberScore, RepProfile } from "@/lib/scorecard";
import { districtName } from "@/lib/districts";
import { ordinal } from "@/lib/graph";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { MemberPhoto } from "@/components/member/member-photo";
import { PartyIndicator } from "@/components/member/party-indicator";
import { VoteBadge } from "@/components/member/vote-badge";

interface RepCardProps {
  rep: LocalRep;
  /**
   * Scored in the browser from the visitor's votes and the member's public positions. Absent
   * before the visitor has voted (the card then says how to fill it in); "loading" while the
   * member's positions load.
   */
  score?: MemberScore | "loading";
  /** How often published Just a Bill users in the visitor's constituency agreed with the rep (#126). */
  alignment?: RepAlignment | null;
  /** The member's party and photo, from GET /members/{id}; initials and no party until it loads. */
  profile?: RepProfile;
}

/** How many compared bills a card lists before "Show all": three side by side, none on a phone. */
const BILLS_SHOWN_WIDE = 3;
const BILLS_SHOWN_NARROW = 0;

/** Tailwind's sm breakpoint: below it the cards stack, so each one folds its bills away. */
const WIDE_QUERY = "(min-width: 640px)";

function wideQuery(): MediaQueryList | null {
  return typeof window.matchMedia === "function" ? window.matchMedia(WIDE_QUERY) : null;
}

function subscribeWide(onChange: () => void): () => void {
  const mq = wideQuery();
  mq?.addEventListener("change", onChange);
  return () => mq?.removeEventListener("change", onChange);
}

/**
 * Whether the viewport is at least sm; true on the server (so the first paint matches desktop)
 * and where matchMedia is missing.
 */
function useWide(): boolean {
  return useSyncExternalStore(subscribeWide, () => wideQuery()?.matches ?? true, () => true);
}

/**
 * One representative (#667): who they are, how often they voted with the visitor, and the bills
 * compared. The same card shows before any votes, with an empty score.
 */
export function RepCard({ rep, score, alignment, profile }: RepCardProps) {
  return (
    <Card className="flex h-full flex-col overflow-hidden">
      <CardHeader className="p-4 pb-3 sm:p-6 sm:pb-4">
        <RepIdentity rep={rep} profile={profile} />
      </CardHeader>
      <CardContent className="flex flex-1 flex-col gap-3 p-4 pt-0 sm:gap-4 sm:p-6 sm:pt-0">
        {score === undefined && <NoScoreYet />}
        {score === "loading" && (
          <Skeleton className="h-28 w-full rounded-lg" role="status" aria-label={`Loading ${rep.name}'s votes`} />
        )}
        {score !== undefined && score !== "loading" && (
          <>
            <VotedWithYou score={score} />
            {alignment && <AlignmentLine rep={rep} alignment={alignment} />}
            <ComparedBills rows={score.rows} />
          </>
        )}
      </CardContent>
    </Card>
  );
}

/** Photo, name, party, chamber and seat. */
function RepIdentity({ rep, profile }: { rep: LocalRep; profile?: RepProfile }) {
  const seat =
    rep.chamber === "Senate" || rep.district === undefined || rep.district === null
      ? rep.state
      : districtName(rep.state, rep.district);
  const party = rep.party || profile?.party || "";
  return (
    <div className="flex items-center gap-3 sm:gap-4">
      <MemberPhoto name={rep.name} photoUrl={profile?.photoUrl} />
      <div className="min-w-0">
        <h3 className="text-lg font-semibold leading-tight text-foreground">
          <Link href={`/members/${rep.id}`} className="transition-colors hover:text-link">
            {rep.name}
          </Link>
        </h3>
        {/* One line on a phone (party · chamber · seat), two from sm up. */}
        <p className="mt-1 flex flex-wrap items-center gap-x-1.5 text-sm text-muted-foreground sm:block">
          {party && (
            <>
              <span className="sm:block">
                <PartyIndicator party={party} showLabel />
              </span>
              <span aria-hidden="true" className="sm:hidden">
                ·
              </span>
            </>
          )}
          <span className="sm:mt-0.5 sm:block">
            {rep.chamber} · {seat}
          </span>
        </p>
      </div>
    </div>
  );
}

/** Before any votes: an empty score that says how it fills in. */
function NoScoreYet() {
  return (
    <div className="rounded-lg border border-dashed border-border p-3 sm:p-4">
      <p className="flex items-baseline justify-between gap-2 text-sm font-medium text-foreground sm:block">
        Voted with you
        <span className="text-2xl font-bold text-muted-foreground sm:mt-1 sm:block sm:text-3xl" aria-hidden="true">
          —
        </span>
      </p>
      <p className="mt-1 text-sm text-muted-foreground">
        Fills in when you vote on a bill they voted on.
      </p>
    </div>
  );
}

/** "Voted with you on 7 of 9 bills", the percentage beside it, and what wasn't counted. */
function VotedWithYou({ score }: { score: MemberScore }) {
  const pct = score.alignment_pct;
  return (
    <div className="rounded-lg bg-muted/50 p-3 sm:p-4">
      <p className="text-sm font-medium text-foreground">Voted with you</p>
      {score.compared === 0 ? (
        <>
          <p className="mt-1 text-3xl font-bold text-muted-foreground" aria-hidden="true">
            —
          </p>
          <p className="mt-1 text-sm text-muted-foreground">No bills in common with a yes or no vote yet.</p>
        </>
      ) : (
        <>
          <p className="mt-1 flex items-baseline justify-between gap-2">
            <span className="text-2xl font-bold text-foreground sm:text-3xl">
              {score.matching} <span className="text-lg font-semibold text-muted-foreground">of {score.compared}</span>
            </span>
            <span className="text-lg font-semibold text-foreground">{pct}%</span>
          </p>
          <div className="mt-2 h-2 w-full overflow-hidden rounded-full bg-muted" aria-hidden="true">
            <div className="h-full rounded-full bg-success" style={{ width: `${pct ?? 0}%` }} />
          </div>
          {/* On a phone "4 of 5" and the bar say it; the sentence repeats it, so it's sm up only. */}
          <p className="mt-2 hidden text-xs text-muted-foreground sm:block">
            On {score.matching} of the {score.compared} {score.compared === 1 ? "bill" : "bills"} you both voted yes or
            no on.
          </p>
        </>
      )}
      {score.member_absent > 0 && (
        <p className="mt-1 text-xs text-muted-foreground">
          {score.member_absent} more not counted (they didn&apos;t vote yes or no).
        </p>
      )}
    </div>
  );
}

/**
 * The bills compared, newest first: a few, then the rest behind "Show all". On a phone, where the
 * cards stack, the list starts folded so the three scores fit on about one screen.
 */
function ComparedBills({ rows }: { rows: VoteComparison[] }) {
  const [showAll, setShowAll] = useState(false);
  const wide = useWide();
  const listId = useId();
  if (rows.length === 0) return null;
  const preview = wide ? BILLS_SHOWN_WIDE : BILLS_SHOWN_NARROW;
  const shown = showAll ? rows : rows.slice(0, preview);
  const label = showAll
    ? "Show fewer"
    : preview === 0
      ? `See the ${rows.length} ${rows.length === 1 ? "bill" : "bills"} compared`
      : `Show all ${rows.length} bills`;
  return (
    <div>
      {shown.length > 0 && (
        <>
          <h4 className="text-sm font-medium text-foreground">Bills compared</h4>
          <ul id={listId} className="mt-2 divide-y divide-border">
            {shown.map((r) => (
              <ComparedBill key={r.bill_id} row={r} />
            ))}
          </ul>
        </>
      )}
      {rows.length > preview && (
        <Button
          variant="ghost"
          size="sm"
          className="mt-1 w-full"
          aria-expanded={showAll}
          aria-controls={shown.length > 0 ? listId : undefined}
          onClick={() => setShowAll(!showAll)}
        >
          {label}
          <ChevronIcon className={`ml-1 h-4 w-4 transition-transform motion-reduce:transition-none ${showAll ? "rotate-180" : ""}`} />
        </Button>
      )}
    </div>
  );
}

function ComparedBill({ row }: { row: VoteComparison }) {
  return (
    <li className="flex items-start gap-3 py-2.5">
      <span className="mt-0.5 shrink-0">
        {!row.counted ? (
          <span className="block h-5 w-5 rounded-full border-2 border-dashed border-muted-foreground" aria-hidden="true" />
        ) : row.matches ? (
          <CheckIcon className="h-5 w-5 text-success" />
        ) : (
          <XIcon className="h-5 w-5 text-destructive" />
        )}
        <span className="sr-only">{!row.counted ? "Not counted:" : row.matches ? "Match:" : "No match:"}</span>
      </span>
      <span className="min-w-0 flex-1">
        <Link
          href={`/bills/${row.bill_id}`}
          className="line-clamp-2 text-sm font-medium text-foreground transition-colors hover:text-link"
        >
          {row.bill_title}
        </Link>
        <span className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
          <span className="inline-flex items-center gap-1">
            You <VoteBadge vote={row.user_vote} />
          </span>
          <span className="inline-flex items-center gap-1">
            They <VoteBadge vote={row.member_vote} />
          </span>
          {row.congress !== undefined && (
            <span>
              {ordinal(row.congress)} Congress{row.chamber ? ` · ${row.chamber}` : ""}
            </span>
          )}
        </span>
      </span>
    </li>
  );
}

/**
 * "Just a Bill users in your district (CA-12) agreed with Rep. X on 14 of 22 bills": the share of
 * the bills where the constituency's published majority matched the rep's Yea or Nay (design #89).
 */
export function AlignmentLine({ rep, alignment }: { rep: LocalRep; alignment: RepAlignment }) {
  const where = rep.chamber === "Senate" ? "state" : "district";
  return (
    <div className="rounded-lg bg-muted/50 p-3 text-sm">
      <p className="text-foreground">
        Just a Bill users in your {where} ({scopeName(alignment.scope_key)}) agreed with {rep.name} on{" "}
        {alignment.bills_agreed} of {alignment.bills_compared} {alignment.bills_compared === 1 ? "bill" : "bills"} in
        the {ordinal(alignment.congress)} Congress.
      </p>
      <p className="mt-1 text-xs text-muted-foreground">
        {AGGREGATES_LABEL}{" "}
        <Link href={METHODOLOGY_AGGREGATES} className="underline underline-offset-2 hover:text-foreground">
          How these numbers work
        </Link>
      </p>
    </div>
  );
}

function ChevronIcon({ className }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2} aria-hidden="true">
      <path strokeLinecap="round" strokeLinejoin="round" d="M19.5 8.25l-7.5 7.5-7.5-7.5" />
    </svg>
  );
}

function CheckIcon({ className }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2} aria-hidden="true">
      <path strokeLinecap="round" strokeLinejoin="round" d="M4.5 12.75l6 6 9-13.5" />
    </svg>
  );
}

function XIcon({ className }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2} aria-hidden="true">
      <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
    </svg>
  );
}
