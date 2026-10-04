"use client";

import { useId, useMemo } from "react";
import Link from "next/link";
import { usePathname, useSearchParams } from "next/navigation";
import { congressHref, congressLabel, congressYearsLabel, selectedCongresses } from "@/lib/congress";

/**
 * The congresses the page's `?congress=` filter picks out of `offered`, newest first; empty for
 * all of them. It reads the URL, so a component that calls it needs a Suspense boundary above it
 * on a prerendered page.
 */
export function useCongressSelection(offered: readonly number[]): number[] {
  const searchParams = useSearchParams();
  const key = searchParams.getAll("congress").join(",");
  return useMemo(() => selectedCongresses(key ? key.split(",") : [], offered), [key, offered]);
}

interface CongressSwitchProps {
  /** The visitor's counted (yea or nay) votes per congress (`votesByCongress`). */
  counts: Readonly<Record<number, number>>;
  /** The congresses with roll calls loaded, newest first (`switchCongresses`). */
  offered: readonly number[];
  /** What the page compares (`useCongressSelection`), newest first; empty for all of them. */
  selected: readonly number[];
}

interface Option {
  key: string;
  /** What the segment shows: "All" or the congress's years. */
  text: string;
  /** Its accessible name: the visible text first, then the congress's number and the count. */
  name: string;
  count: number;
  selection: number[];
}

/**
 * The congresses the switch offers: those with roll calls loaded that the visitor has counted
 * votes in, newest first. A congress they only skipped, or never voted in, has nothing to compare.
 */
export function switchChoices(counts: Readonly<Record<number, number>>, offered: readonly number[]): number[] {
  return offered.filter((n) => (counts[n] ?? 0) > 0);
}

function votesWord(count: number): string {
  return count === 1 ? "vote" : "votes";
}

/** "All" first, since it's the scorecard's default, then each congress, newest first. */
function switchOptions(counts: Readonly<Record<number, number>>, choices: readonly number[]): Option[] {
  const total = Object.values(counts).reduce((sum, n) => sum + n, 0);
  return [
    { key: "all", text: "All", name: `All, ${total} ${votesWord(total)}`, count: total, selection: [] },
    ...choices.map((n) => ({
      key: String(n),
      text: congressYearsLabel(n),
      name: `${congressLabel(n)}, ${counts[n]} ${votesWord(counts[n])}`,
      count: counts[n],
      selection: [n],
    })),
  ];
}

/** Whether an option is what the page compares: "All" for no filter or one naming every choice. */
function isActive(option: Option, selected: readonly number[], choices: readonly number[]): boolean {
  if (option.selection.length === 0) {
    return selected.length === 0 || (selected.length === choices.length && choices.every((n) => selected.includes(n)));
  }
  return selected.length === 1 && selected[0] === option.selection[0];
}

/**
 * The scorecard's congress switch (#243, redone in #844): "Compare your votes on bills from",
 * then one segment per choice with the number of the visitor's votes it compares, bound to
 * `?congress=` so reloads and shared links keep the choice. It renders nothing unless the
 * visitor has counted votes in two or more congresses: there's nothing to choose. Three
 * segments fit a 320px phone; a fourth wraps to a second row.
 */
export function CongressSwitch({ counts, offered, selected }: CongressSwitchProps) {
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const labelId = useId();
  const choices = switchChoices(counts, offered);
  if (choices.length < 2) return null;

  const search = searchParams.toString();
  const options = switchOptions(counts, choices);

  return (
    <div className="space-y-2">
      <p id={labelId} className="text-sm font-medium text-foreground">
        Compare your votes on bills from
      </p>
      <nav
        aria-labelledby={labelId}
        className="grid grid-cols-3 gap-1 rounded-xl bg-muted p-1 sm:inline-grid sm:grid-flow-col sm:auto-cols-fr sm:grid-cols-none"
      >
        {options.map((o) => {
          const active = isActive(o, selected, choices);
          return (
            <Link
              key={o.key}
              href={congressHref(pathname, search, o.selection)}
              scroll={false}
              aria-current={active ? "true" : undefined}
              aria-label={o.name}
              className={`rounded-lg px-3 py-2 text-center transition-colors motion-reduce:transition-none sm:min-w-28 sm:px-5 ${
                active ? "bg-card shadow-sm" : "hover:bg-card/60"
              }`}
            >
              <span className={`block text-sm font-semibold ${active ? "text-foreground" : "text-muted-foreground"}`}>
                {o.text}
              </span>
              <span className="block text-xs tabular-nums text-muted-foreground">
                {o.count} {votesWord(o.count)}
              </span>
            </Link>
          );
        })}
      </nav>
    </div>
  );
}
