"use client";

import Link from "next/link";
import type { MyVotesQuery, MyVoteSummary } from "@/lib/my-votes";
import type { UserVoteChoice } from "@/lib/types";

const CHOICES: readonly { key: UserVoteChoice; label: string; fill: string }[] = [
  { key: "yea", label: "Yea", fill: "bg-vote-yea" },
  { key: "nay", label: "Nay", fill: "bg-vote-nay" },
  { key: "skip", label: "Skipped", fill: "bg-vote-skip" },
];

const FIGURE =
  "inline-flex min-h-9 items-center gap-2 rounded-lg border border-border px-3 text-sm text-foreground hover:bg-muted " +
  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring aria-pressed:border-primary aria-pressed:bg-muted";

export interface VoteSummaryProps {
  summary: MyVoteSummary;
  query: MyVotesQuery;
  /** Whether the bills' statuses are still loading; Became law waits for them. */
  loading: boolean;
  /** Where the votes are kept, in a sentence. */
  note: React.ReactNode;
  onQuery: (next: MyVotesQuery) => void;
}

/**
 * The record at a glance (#843): how many votes, how they split, and how many of those bills
 * became law. The split is one bar; each figure under it is a button that filters the list to it
 * (and back, pressed again), so the words and numbers carry everything the bar's colors show.
 */
export function VoteSummary({ summary, query, loading, note, onQuery }: VoteSummaryProps) {
  const { total, becameLaw } = summary;
  return (
    <section aria-labelledby="my-votes-summary" className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-1">
        <h2 id="my-votes-summary" className="text-lg font-semibold tabular-nums text-foreground">
          {`${total.toLocaleString()} ${total === 1 ? "vote" : "votes"}`}
        </h2>
        <Link href="/scorecard" className="inline-flex min-h-6 items-center text-sm text-link underline underline-offset-2">
          Compare with your representatives
        </Link>
      </div>

      <div className="flex h-3 gap-0.5 overflow-hidden rounded-full" aria-hidden="true">
        {CHOICES.map(
          (c) => summary[c.key] > 0 && <div key={c.key} className={c.fill} style={{ flexGrow: summary[c.key], flexBasis: 0 }} />,
        )}
      </div>

      <div className="flex flex-wrap gap-2">
        {CHOICES.map((c) => (
          <button
            key={c.key}
            type="button"
            className={FIGURE}
            aria-pressed={query.vote === c.key}
            onClick={() => onQuery({ ...query, vote: query.vote === c.key ? "all" : c.key })}
          >
            <span className={`h-2.5 w-2.5 rounded-sm ${c.fill}`} aria-hidden="true" />
            {c.label}{" "}
            <span className="font-semibold tabular-nums">{summary[c.key].toLocaleString()}</span>
          </button>
        ))}
        {becameLaw !== null || loading ? (
          <button
            type="button"
            className={`${FIGURE} disabled:opacity-60 sm:ml-auto`}
            aria-pressed={query.status === "laws"}
            disabled={becameLaw === null}
            onClick={() => onQuery({ ...query, status: query.status === "laws" ? "all" : "laws" })}
          >
            Became law{" "}
            {becameLaw === null ? (
              <span className="inline-block h-4 w-6 animate-pulse rounded bg-muted" aria-hidden="true" />
            ) : (
              <span className="font-semibold tabular-nums">{becameLaw.toLocaleString()}</span>
            )}
          </button>
        ) : (
          // Some list didn't load: no count rather than one that leaves those bills out.
          <p className="inline-flex min-h-9 items-center text-sm text-muted-foreground sm:ml-auto">
            Where some bills stand couldn&apos;t be loaded.
          </p>
        )}
      </div>

      <p className="text-sm text-muted-foreground">{note}</p>
    </section>
  );
}
