import { ordinal } from "./graph";
import type { Congress } from "./types";

/**
 * The congress now in session: the one the API marks current, or else the highest-numbered one.
 * Undefined when there are none (an empty database).
 */
export function currentCongress(congresses: Congress[]): Congress | undefined {
  const marked = congresses.find((c) => c.is_current);
  if (marked) return marked;
  let latest: Congress | undefined;
  for (const c of congresses) {
    if (!latest || c.number > latest.number) latest = c;
  }
  return latest;
}

/** The first year of a congress: the 1st met in 1789, and each lasts two years. */
function firstYear(congress: number): number {
  return 1789 + 2 * (congress - 1);
}

/** A congress by its years alone, where the number would crowd a control: "2025–26". */
export function congressYearsLabel(congress: number): string {
  const start = firstYear(congress);
  return `${start}–${String((start + 1) % 100).padStart(2, "0")}`;
}

/** A congress by its years and number, as the congress filters name it: "2025–26 (119th)". */
export function congressLabel(congress: number): string {
  return `${congressYearsLabel(congress)} (${ordinal(congress)})`;
}

/**
 * The congresses the scorecard and /vote switch offers (#243): those with roll calls loaded,
 * newest first. With fewer than two there's nothing to switch between.
 */
export function switchCongresses(congresses: readonly Congress[]): number[] {
  return congresses
    .filter((c) => c.has_votes)
    .map((c) => c.number)
    .sort((a, b) => b - a);
}

/**
 * The congresses a `?congress=` filter picks out of the offered ones, newest first. Values that
 * aren't offered are ignored. Empty means all of them ("Both"), which is also what a filter
 * naming every offered congress comes to.
 */
export function selectedCongresses(values: readonly string[], offered: readonly number[]): number[] {
  const picked = offered.filter((n) => values.includes(String(n)));
  return picked.length === offered.length ? [] : picked;
}

/** `?congress=all`: every congress, on the pages that show the current one by default (#717). */
export const ALL_CONGRESSES = "all";

/**
 * The congresses /vote draws from: the newest offered one unless `?congress=` names others, and
 * every one for `?congress=all` (#717). Empty means all of them, as in `selectedCongresses`.
 */
export function defaultedCongresses(values: readonly string[], offered: readonly number[]): number[] {
  if (values.includes(ALL_CONGRESSES)) return [];
  const picked = offered.filter((n) => values.includes(String(n)));
  if (picked.length === 0) return offered.slice(0, 1);
  return picked.length === offered.length ? [] : picked;
}

/**
 * The `?congress=` value for a choice on a page that defaults to `fallback`: none for the
 * default itself, `all` for every congress, or the congress's number.
 */
export function congressParam(choice: string, fallback: number | undefined): string | null {
  if (choice === "" || choice === String(fallback)) return null;
  return choice;
}

/**
 * `pathname` with `search`'s `?congress=` replaced by `selection` (none for all congresses),
 * keeping every other parameter.
 */
export function congressHref(pathname: string, search: string, selection: readonly number[]): string {
  const params = new URLSearchParams(search);
  params.delete("congress");
  for (const n of selection) params.append("congress", String(n));
  const qs = params.toString();
  return qs ? `${pathname}?${qs}` : pathname;
}
