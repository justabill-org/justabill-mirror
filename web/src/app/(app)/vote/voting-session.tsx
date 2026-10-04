"use client";

import { useState, useCallback } from "react";
import Link from "next/link";
import type { PaginatedResult, UserVoteChoice } from "@/lib/types";
import { SwipeCard } from "@/components/vote/swipe-card";
import { Button, buttonClasses } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useUser } from "@/lib/auth/provider";
import { billFiltersQuery, type BillFilters } from "@/lib/bill-filters";
import { parseBillView } from "@/lib/bill-views";
import { trackConversion } from "@/lib/experiments/client";
import { VOTE_AA } from "@/lib/experiments/registry";
import { SPAN_VOTE_CAST, traceAction } from "@/lib/obs/browser";
import { isVoteCapReached, VOTE_CAP_REACHED, VOTE_FAILED, type VoteStorage } from "@/lib/votes/backend";
import { useVoteBackend, useVotes } from "@/lib/votes/hooks";
import type { DeckItem } from "@/lib/vote-deck";
import { useVoteDeck, type VoteDeck } from "./use-vote-deck";

const STORAGE_LINES: Partial<Record<VoteStorage, string>> = {
  device: "Your votes are kept on this device only.",
  memory: "Votes won't be saved on this browser.",
  account: "Your votes are saved in your account.",
};

interface VotingSessionProps {
  /** The deck's filters, as the URL gives them. */
  filters: BillFilters;
  /** The congress in session, which the filters' default congress stands for. */
  current: number | undefined;
  /** The default deck's first batch, rendered into the page; only used with the default filters. */
  first?: PaginatedResult<DeckItem>;
  /** Opens the filter, for "Change filters" at the end of the deck. */
  onChangeFilters?: () => void;
}

/**
 * The /vote deck (#797): every bill under the filters, one card at a time, until the list ends.
 * It starts once the user's votes are known (they're empty during server render and hydration),
 * and starts over when the user signs in or out.
 */
export function VotingSession(props: VotingSessionProps) {
  const backend = useVoteBackend();
  const { storage } = useVotes();

  if (storage === "unavailable") {
    return (
      <div role="alert" className="flex flex-col items-center justify-center py-16 text-center">
        <p className="text-destructive">We couldn&apos;t load your votes from your account.</p>
        {backend.refresh && (
          <Button variant="outline" className="mt-4" onClick={backend.refresh}>
            Try again
          </Button>
        )}
      </div>
    );
  }
  if (storage === "server") return <LoadingDeck />;
  const signedIn = storage === "account";
  return <DeckSession key={signedIn ? "account" : "device"} signedIn={signedIn} {...props} />;
}

function DeckSession({
  filters,
  current,
  first,
  onChangeFilters,
  signedIn,
}: VotingSessionProps & { signedIn: boolean }) {
  const backend = useVoteBackend();
  const { votes, storage } = useVotes();
  const { getIdToken } = useUser();
  const query = billFiltersQuery(filters);
  const deck = useVoteDeck(filters, {
    current,
    first: query === "" ? first : undefined,
    getIdToken: signedIn ? getIdToken : undefined,
    votes,
  });
  const [votedCount, setVotedCount] = useState(0);
  const [isAnimating, setIsAnimating] = useState(false);
  // The choice whose save failed, kept so Try again resends it. The card stays put until a save
  // succeeds; each failure bumps `attempt`, which remounts the card the click animated out.
  const [failed, setFailed] = useState<UserVoteChoice | null>(null);
  // The save was refused for the daily vote cap (#646): every vote fails the same way until the
  // window moves, so the card stays and there's no Try again.
  const [capped, setCapped] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const card = deck.card;
  const { next } = deck;

  const handleVote = useCallback(
    async (vote: UserVoteChoice) => {
      if (isAnimating || !card) return;

      setIsAnimating(true);
      setFailed(null);
      setCapped(false);

      // Signed out, the vote is kept in this browser only (lib/local/votes.ts); signed in, it goes
      // to the account (lib/votes/account.ts). Skips are kept too, so the bill isn't offered again.
      try {
        await traceAction(SPAN_VOTE_CAST, () => backend.setVote(card.bill.id, vote, card.bill.title));
      } catch (err) {
        if (isVoteCapReached(err)) setCapped(true);
        else setFailed(vote);
        setAttempt((prev) => prev + 1);
        setIsAnimating(false);
        return;
      }

      if (vote !== "skip") {
        setVotedCount((prev) => prev + 1);
        // The A/A run's goal (#695): a first vote cast here, sent once, without the choice.
        trackConversion(VOTE_AA);
      }

      // Wait for animation
      setTimeout(() => {
        next();
        setIsAnimating(false);
      }, 300);
    },
    [backend, card, isAnimating, next]
  );

  const listHref = query ? `/bills?${query}` : "/bills";

  return (
    <div className="space-y-4">
      {deck.total !== undefined && deck.status !== "empty" && (
        <DeckProgress deck={deck} label={parseBillView(filters.view).label} voted={votedCount} listHref={listHref} />
      )}

      <DeckBody
        deck={deck}
        voted={votedCount}
        query={query}
        listHref={listHref}
        onChangeFilters={onChangeFilters}
      />

      {card && (
        <>
          <SwipeCard
            key={`${card.bill.id}:${attempt}`}
            bill={card.bill}
            summary={card.summary}
            card={card.card}
            onVote={handleVote}
            isAnimating={isAnimating}
          />

          {capped && (
            <p role="alert" className="text-center text-sm text-destructive">
              {VOTE_CAP_REACHED}
            </p>
          )}

          {failed && (
            <div role="alert" className="flex flex-wrap items-center justify-center gap-3">
              <p className="text-sm text-destructive">{VOTE_FAILED[failed]}</p>
              <Button variant="outline" size="sm" onClick={() => handleVote(failed)}>
                Try again
              </Button>
            </div>
          )}

          <p className={`text-center text-sm ${storage === "memory" ? "text-destructive" : "text-muted-foreground"}`}>
            {STORAGE_LINES[storage] ?? STORAGE_LINES.device}
          </p>
        </>
      )}
    </div>
  );
}

/** What the deck holds, as /bills says it, how much of it is left, and the votes cast this visit. */
function DeckProgress({
  deck,
  label,
  voted,
  listHref,
}: {
  deck: VoteDeck;
  label: string;
  voted: number;
  listHref: string;
}) {
  const total = deck.total ?? 0;
  const left = Math.min(deck.left ?? total, total);
  const progress = total > 0 ? ((total - left) / total) * 100 : 0;
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 text-sm">
        <p className="text-muted-foreground">
          {label}: {left.toLocaleString("en-US")} of {total.toLocaleString("en-US")} bills left
        </p>
        <p className="flex items-center gap-3">
          <span className="font-medium text-foreground">{voted} voted this visit</span>
          <Link href={listHref} className="text-link underline-offset-2 hover:underline">
            See as a list
          </Link>
        </p>
      </div>
      <div className="h-2 w-full overflow-hidden rounded-full bg-muted">
        <div className="h-full bg-foreground transition-all duration-300" style={{ width: `${progress}%` }} />
      </div>
    </div>
  );
}

/** The card area when there's no card to deal: loading, a failed read, the end, or nothing found. */
function DeckBody({
  deck,
  voted,
  query,
  listHref,
  onChangeFilters,
}: {
  deck: VoteDeck;
  voted: number;
  query: string;
  listHref: string;
  onChangeFilters?: () => void;
}) {
  const changeFilters = onChangeFilters && (
    <Button variant="outline" onClick={onChangeFilters}>
      Change filters
    </Button>
  );
  switch (deck.status) {
    case "ready":
      return null;
    case "loading":
      return <LoadingDeck />;
    case "failed":
      return (
        <div role="alert" className="flex flex-col items-center justify-center rounded-xl border border-border bg-card py-16 text-center">
          <p className="max-w-xs text-foreground">We couldn&apos;t load more bills. Check your connection and try again.</p>
          <Button variant="outline" className="mt-4" onClick={deck.retry}>
            Try again
          </Button>
        </div>
      );
    case "empty":
      return (
        <div className="flex flex-col items-center justify-center rounded-xl border border-dashed border-border py-16 text-center">
          <h2 className="text-lg font-semibold text-foreground">No bills found</h2>
          <p className="mt-2 max-w-xs text-muted-foreground">
            {query ? "No bill matches these filters." : "Check back later for new legislation."}
          </p>
          {query && (
            <Link href="/vote" className={`mt-4 ${buttonClasses({ variant: "outline" })}`}>
              Clear filters
            </Link>
          )}
        </div>
      );
    case "capped":
      return (
        <div className="flex flex-col items-center justify-center rounded-xl border border-border bg-card px-4 py-16 text-center">
          <h2 className="text-lg font-semibold text-foreground">That&apos;s as far as this list goes</h2>
          <p className="mt-2 max-w-sm text-muted-foreground">
            This list has more bills than we can go through here. Narrow the filters, for example by congress, bill type
            or policy area, to see the rest.
          </p>
          <div className="mt-6 flex flex-wrap justify-center gap-3">
            {changeFilters}
            <Link href={listHref} className={buttonClasses({ variant: "outline" })}>
              See as a list
            </Link>
          </div>
        </div>
      );
    case "end":
      return (
        <div className="flex flex-col items-center justify-center rounded-xl border border-border bg-card px-4 py-16 text-center">
          <div className="flex h-16 w-16 items-center justify-center rounded-full bg-success/10">
            <CheckIcon className="h-8 w-8 text-success" />
          </div>
          <h2 className="mt-4 text-xl font-semibold text-foreground">You&apos;ve voted on every bill here</h2>
          <p className="mt-2 max-w-xs text-muted-foreground">
            You&apos;ve voted on or skipped every bill under these filters
            {voted > 0 ? `, ${voted} of them on this visit` : ""}.
          </p>
          <div className="mt-6 flex flex-wrap justify-center gap-3">
            {changeFilters}
            <Link href="/my-votes" className={buttonClasses({ variant: "outline" })}>
              My votes
            </Link>
            <Link href="/scorecard" className={buttonClasses()}>
              Scorecard
            </Link>
          </div>
        </div>
      );
  }
}

function LoadingDeck() {
  return <Skeleton className="h-96 w-full rounded-xl" role="status" aria-label="Loading bills" />;
}

function CheckIcon({ className }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5} aria-hidden="true">
      <path strokeLinecap="round" strokeLinejoin="round" d="M9 12.75L11.25 15 15 9.75M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
    </svg>
  );
}
