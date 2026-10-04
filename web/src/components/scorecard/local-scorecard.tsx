"use client";

import { useEffect, useMemo, useState, type ReactNode } from "react";
import Link from "next/link";
import { Card, CardContent } from "@/components/ui/card";
import { Button, buttonClasses } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { CongressSwitch, useCongressSelection } from "@/components/congress/congress-switch";
import { RepCard } from "@/components/scorecard/rep-card";
import { FindRepsForm } from "@/components/scorecard/find-reps-form";
import { ElectionDistrictNote } from "@/components/member/election-district-note";
import { congressHref } from "@/lib/congress";
import { noSenatorsNote, seatsLabel } from "@/lib/districts";
import { ordinal } from "@/lib/graph";
import type { LocalReps } from "@/lib/local/reps";
import type { LocalVotes } from "@/lib/local/votes";
import {
  congressesVotedIn,
  loadPositions,
  loadRepProfiles,
  score,
  votesByCongress,
  type MemberScore,
  type PositionsByMember,
  type RepProfile,
} from "@/lib/scorecard";
import { SPAN_COMPARE_RUN, traceAction } from "@/lib/obs/browser";
import { getMemberAlignment } from "@/lib/api";
import { pickAlignment } from "@/lib/aggregates";
import type { RepAlignment } from "@/lib/types";
import type { VoteStorage } from "@/lib/votes/backend";
import { localReps, useLocalReps, useVoteBackend, useVotes } from "@/lib/votes/hooks";

/** The positions for one set of members × congresses, keyed so a stale load is never shown. */
type Loaded = { key: string; positions: PositionsByMember } | { key: string; error: true };

export type ScoresState = { status: "none" } | { status: "loading" } | { status: "error" } | {
  status: "ready";
  scores: Record<string, MemberScore>;
};

/** Each rep's alignment rows by member ID, keyed by the members they were loaded for. */
type Alignments = { key: string; rows: Record<string, RepAlignment[]> };

/**
 * Loads each rep's published alignment with users in their constituency (#126). Optional: a rep
 * whose request fails (or every rep, while the API has aggregates off) just has no line.
 */
function useAlignments(members: LocalReps["members"] | undefined): Record<string, RepAlignment[]> {
  const key = members ? members.map((m) => m.id).join(",") : "";
  const [loaded, setLoaded] = useState<Alignments | null>(null);
  useEffect(() => {
    if (!key) return;
    let cancelled = false;
    const ids = key.split(",");
    Promise.allSettled(ids.map((id) => getMemberAlignment(id))).then((results) => {
      if (cancelled) return;
      const rows: Record<string, RepAlignment[]> = {};
      results.forEach((r, i) => {
        if (r.status === "fulfilled") rows[ids[i]] = r.value.alignment;
      });
      setLoaded({ key, rows });
    });
    return () => {
      cancelled = true;
    };
  }, [key]);
  return loaded && loaded.key === key ? loaded.rows : {};
}

/** Each rep's party and photo by member ID, keyed by the members they were loaded for. */
type Profiles = { key: string; profiles: Record<string, RepProfile> };

/**
 * Loads each rep's party and photo (#667), which the address lookup doesn't return. Optional: a
 * rep whose request fails shows initials and no party.
 */
function useRepProfiles(members: LocalReps["members"] | undefined): Record<string, RepProfile> {
  const key = members ? members.map((m) => `${m.id}:${m.chamber}`).join(",") : "";
  const [loaded, setLoaded] = useState<Profiles | null>(null);
  useEffect(() => {
    if (!key) return;
    let cancelled = false;
    const wanted = key.split(",").map((k) => {
      const [id, chamber] = k.split(":");
      return { id, chamber };
    });
    loadRepProfiles(wanted).then((profiles) => {
      if (!cancelled) setLoaded({ key, profiles });
    });
    return () => {
      cancelled = true;
    };
  }, [key]);
  return loaded && loaded.key === key ? loaded.profiles : {};
}

/**
 * The signed-out scorecard (#215): reps from this browser, votes from this browser, and each
 * member's public positions, scored here. Only the one rep lookup and the members' public GETs
 * leave the page, and none carries a vote.
 */
export function LocalScorecard({ offered = [] }: { offered?: readonly number[] }) {
  const { votes, storage } = useVotes();
  const reps = useLocalReps();
  const selected = useCongressSelection(offered);
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const [attempt, setAttempt] = useState(0);

  // The congresses the visitor voted in, narrowed by the switch (#243); stable while new votes
  // stay in the same congresses, so voting doesn't refetch.
  const votedIn = congressesVotedIn(votes);
  const congressKey = (selected.length > 0 ? votedIn.filter((c) => selected.includes(c)) : votedIn).join(",");
  const congresses = useMemo(() => (congressKey ? congressKey.split(",").map(Number) : []), [congressKey]);
  const members = reps?.members;
  const key = members && congresses.length > 0 ? `${members.map((m) => m.id).join(",")}|${congressKey}|${attempt}` : "";
  // Only once there are votes to compare, the only time the cards show.
  const alignments = useAlignments(key ? members : undefined);
  const profiles = useRepProfiles(members);

  useEffect(() => {
    if (!key || !members) return;
    let cancelled = false;
    traceAction(SPAN_COMPARE_RUN, () => loadPositions(members, congresses))
      .then((positions) => {
        if (!cancelled) setLoaded({ key, positions });
      })
      .catch(() => {
        if (!cancelled) setLoaded({ key, error: true });
      });
    return () => {
      cancelled = true;
    };
  }, [key, members, congresses]);

  let scores: ScoresState = { status: "none" };
  if (key) {
    if (!loaded || loaded.key !== key) scores = { status: "loading" };
    else if ("error" in loaded) scores = { status: "error" };
    else scores = { status: "ready", scores: score(votes, loaded.positions) };
  }

  return (
    <ScorecardView
      votes={votes}
      storage={storage}
      reps={reps}
      scores={scores}
      selected={selected}
      alignments={alignments}
      profiles={profiles}
      control={<CongressSwitch counts={votesByCongress(votes)} offered={offered} selected={selected} />}
      onRetry={() => setAttempt((a) => a + 1)}
    />
  );
}

export interface ScorecardViewProps {
  votes: LocalVotes;
  storage: VoteStorage;
  reps: LocalReps | null;
  scores: ScoresState;
  /** The congresses the switch picked, newest first; empty or absent for all of them. */
  selected?: readonly number[];
  /** Each rep's alignment rows with users in their constituency, by member ID (#126). */
  alignments?: Record<string, RepAlignment[]>;
  /** Each rep's party and photo by member ID; a rep without a photo shows their initials. */
  profiles?: Record<string, RepProfile>;
  /** The congress switch, shown under the heading, next to the cards it changes (#844). */
  control?: ReactNode;
  onRetry?: () => void;
}

/** What the scorecard shows for a given state; no hooks that fetch, so tests can render it. */
export function ScorecardView({
  votes,
  storage,
  reps,
  scores,
  selected = [],
  alignments = {},
  profiles = {},
  control,
  onRetry,
}: ScorecardViewProps) {
  if (storage === "server") {
    return <Skeleton className="h-64 w-full rounded-xl" role="status" aria-label="Loading your scorecard" />;
  }
  if (storage === "unavailable") return <VotesUnavailable />;
  const voteCount = Object.values(votes).filter((v) => v.vote !== "skip").length;

  const scoreFor = (id: string): MemberScore | "loading" | undefined => {
    if (scores.status === "loading") return "loading";
    if (scores.status === "ready") return scores.scores[id];
    return undefined;
  };

  return (
    <div className="space-y-8">
      {!reps && <FindMyReps />}

      {reps && (
        <section aria-labelledby="your-reps" className="space-y-4">
          <YourReps reps={reps} />
          {control}
          {voteCount === 0 && <VoteFirst />}
          {voteCount > 0 && scores.status === "none" && selected.length > 0 && <NoVotesInCongress selected={selected} />}
          {scores.status === "error" && (
            <div className="rounded-lg border border-border p-4 text-center">
              <p role="alert" className="text-destructive">
                We couldn&apos;t load your representatives&apos; votes. Please try again.
              </p>
              {onRetry && (
                <Button variant="outline" className="mt-3" onClick={onRetry}>
                  Try again
                </Button>
              )}
            </div>
          )}
          <ul className="grid gap-3 sm:gap-4 md:grid-cols-2 lg:grid-cols-3">
            {reps.members.map((rep) => (
              <li key={rep.id}>
                <RepCard
                  rep={rep}
                  score={scoreFor(rep.id)}
                  alignment={pickAlignment(alignments[rep.id] ?? [], rep, selected)}
                  profile={profiles[rep.id]}
                />
              </li>
            ))}
          </ul>
          <p className="text-sm text-muted-foreground">
            Only each chamber&apos;s final vote on a bill counts, and Present or Not Voting counts neither way. The
            rule is the same for every member.{" "}
            <Link href="/methodology#scorecard" className="underline underline-offset-2 hover:text-foreground">
              How the scorecard works
            </Link>
          </p>
        </section>
      )}

      <MyVotesLine storage={storage} voteCount={voteCount} />
    </div>
  );
}

/** The heading over the cards: where the visitor's representatives are from, and a way to change them. */
function YourReps({ reps }: { reps: LocalReps }) {
  const districts = reps.district === null ? [] : [{ state: reps.state, district: reps.district }];
  const seat = districts.length > 0 ? seatsLabel(districts) : "";
  const hasSenators = reps.members.some((m) => m.chamber === "Senate");
  const note = hasSenators ? null : noSenatorsNote(districts);
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <h2 id="your-reps" className="text-xl font-semibold text-foreground">
          Your representatives
          <span className="ml-2 text-base font-normal text-muted-foreground">
            {reps.state}
            {seat && ` · ${seat}`}
          </span>
        </h2>
        <Button variant="ghost" size="sm" onClick={() => localReps().clearReps()}>
          Change address
        </Button>
      </div>
      {reps.election && (
        <ElectionDistrictNote
          lookup={{ districts: reps.election.current, election: { ...reps.election, changed: true } }}
        />
      )}
      {note && <p className="text-sm text-muted-foreground">{note}</p>}
    </div>
  );
}

function FindMyReps() {
  return (
    <section aria-label="Find your representatives" className="grid gap-6 lg:grid-cols-5">
      <div className="lg:col-span-3">
        <FindRepsForm />
      </div>
      <HowItWorks />
    </section>
  );
}

/** The three steps, so the page explains itself before there's anything to show. */
function HowItWorks() {
  const steps = [
    { title: "Find your representatives", text: "Enter your address once. This device remembers who they are." },
    { title: "Vote on bills", text: "Say Yea or Nay on bills Congress voted on." },
    { title: "See how you line up", text: "Each card shows how often they voted the way you did." },
  ];
  return (
    <div className="lg:col-span-2">
      <h3 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">How it works</h3>
      <ol className="mt-3 space-y-4">
        {steps.map((step, i) => (
          <li key={step.title} className="flex gap-3">
            <span
              aria-hidden="true"
              className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-muted text-sm font-semibold text-foreground"
            >
              {i + 1}
            </span>
            <span>
              <span className="block font-medium text-foreground">{step.title}</span>
              <span className="block text-sm text-muted-foreground">{step.text}</span>
            </span>
          </li>
        ))}
      </ol>
      <p className="mt-4 text-sm text-muted-foreground">
        Or{" "}
        <Link href="/vote" className="font-medium text-foreground underline underline-offset-2 hover:text-link">
          start voting now
        </Link>{" "}
        and find them later.
      </p>
    </div>
  );
}

function VoteFirst() {
  return (
    <div className="flex flex-col gap-4 rounded-xl border border-border bg-muted/40 p-5 sm:flex-row sm:items-center sm:justify-between">
      <div>
        <h3 className="text-lg font-semibold text-foreground">Vote on a few bills to fill in your scorecard</h3>
        <p className="mt-1 text-sm text-muted-foreground">
          Each card below shows how often your representative voted the way you did, bill by bill.
        </p>
      </div>
      <Link href="/vote" className={buttonClasses({ size: "lg", className: "shrink-0" })}>
        Start voting
      </Link>
    </div>
  );
}

function NoVotesInCongress({ selected }: { selected: readonly number[] }) {
  const names = selected.map((n) => `${ordinal(n)} Congress`).join(" or ");
  return (
    <Card className="text-center">
      <CardContent className="py-10">
        <h3 className="text-lg font-semibold text-foreground">No votes from the {names} yet</h3>
        <p className="mx-auto mt-2 max-w-sm text-muted-foreground">
          You haven&apos;t voted on any of its bills. Vote on a few to compare them with your representatives.
        </p>
        <div className="mt-6 flex flex-wrap justify-center gap-3">
          <Link href={congressHref("/vote", "", selected)} className={buttonClasses()}>
            Start voting
          </Link>
          {/* The switch may be hidden (a shared link to a congress the visitor never voted in), so
              the way back to everything is here. */}
          <Link href="/scorecard" scroll={false} className={buttonClasses({ variant: "outline" })}>
            Compare all your votes
          </Link>
        </div>
      </CardContent>
    </Card>
  );
}

function VotesUnavailable() {
  const backend = useVoteBackend();
  return (
    <Card>
      <CardContent className="py-8 text-center">
        <p role="alert" className="text-destructive">
          We couldn&apos;t load your votes from your account.
        </p>
        {backend.refresh && (
          <Button variant="outline" className="mt-4" onClick={backend.refresh}>
            Try again
          </Button>
        )}
      </CardContent>
    </Card>
  );
}

/** Where the votes are kept, and the way to My votes, which lists them with their tools (#739). */
function MyVotesLine({ storage, voteCount }: { storage: VoteStorage; voteCount: number }) {
  const count = `${voteCount} ${voteCount === 1 ? "vote" : "votes"} so far`;
  let where = `${count}, kept on this device only. See, change, download or import them in`;
  if (storage === "account") where = `${count}, saved in your account. See or change them in`;
  else if (storage === "memory") where = `${count}. Votes won't be saved on this browser. See or download them in`;
  return (
    <p className="text-sm text-muted-foreground">
      {where}{" "}
      <Link href="/my-votes" className="font-medium text-foreground underline underline-offset-2">
        My votes
      </Link>
      .
    </p>
  );
}
