/**
 * List paging and search within the API's rules (api/internal/handler/respond.go, #201, #619, #881):
 * offset is a non-negative integer no larger than MAX_OFFSET (MAX_SEARCH_OFFSET in a search), limit
 * is clamped to 1–MAX_LIMIT, a search has at most MAX_SEARCH_TERMS terms and MAX_SEARCH_CHARS
 * characters, and a congress is a whole number from 1 to MAX_CONGRESS. The API answers 400 to
 * anything else, so the web never sends it.
 */

/** The API's largest accepted offset (`maxOffset`): deeper pages need narrower filters. */
export const MAX_OFFSET = 10_000;

/** The API's largest accepted offset in a search (`maxSearchOffset`): every page of one costs more. */
export const MAX_SEARCH_OFFSET = 500;

/** The most characters (code points) the API takes in a search (`maxSearchRunes`). */
export const MAX_SEARCH_CHARS = 100;

/** The most terms (runs of non-space characters) the API takes in a search (`maxSearchTerms`). */
export const MAX_SEARCH_TERMS = 8;

/** The characters Go's unicode.IsSpace counts as space, so terms split here as strings.Fields splits them. */
const GO_SPACE = /[\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+/;

/**
 * The search the API will take for one a visitor typed: its first MAX_SEARCH_TERMS terms, joined by
 * one space and cut to MAX_SEARCH_CHARS characters, or undefined when nothing is left.
 */
export function clampSearch(raw: string | undefined): string | undefined {
  if (raw === undefined) return undefined;
  const terms = raw.split(GO_SPACE).filter(Boolean).slice(0, MAX_SEARCH_TERMS);
  const clamped = [...terms.join(" ")].slice(0, MAX_SEARCH_CHARS).join("").trim();
  return clamped || undefined;
}

/** The largest offset the API accepts for a list with this search (none, or empty, is no search). */
export function maxOffsetFor(search: string | null | undefined): number {
  return search ? MAX_SEARCH_OFFSET : MAX_OFFSET;
}

/** The API's largest congress filter (`maxCongress`): the 200th Congress starts in 2387. */
export const MAX_CONGRESS = 200;

/**
 * A congress filter from a query value, as the API's queryCongress reads it: decimal digits alone
 * (leading zeros allowed) from 1 to MAX_CONGRESS. Anything else is undefined, so a hand-edited URL
 * drops the filter rather than sending a value the API refuses.
 */
export function parseCongress(raw: string | null | undefined): number | undefined {
  if (!raw || !/^\d+$/.test(raw)) return undefined;
  const n = Number(raw);
  return n >= 1 && n <= MAX_CONGRESS ? n : undefined;
}

/** The API's largest page size (`maxPageSize`); it clamps larger limits to this. */
export const MAX_LIMIT = 100;

/** A whole, non-negative number written in decimal digits, as the API's strconv.Atoi reads it. */
function parseNonNegativeInt(raw: string | undefined): number | undefined {
  if (raw === undefined) return undefined;
  const trimmed = raw.trim();
  if (!/^\d+$/.test(trimmed)) return undefined;
  const n = Number(trimmed);
  return Number.isSafeInteger(n) ? n : Number.MAX_SAFE_INTEGER;
}

/** The page size from a query value: fallback when missing, malformed or zero, at most MAX_LIMIT. */
export function parseLimit(raw: string | undefined, fallback: number): number {
  const n = parseNonNegativeInt(raw);
  if (n === undefined || n === 0) return fallback;
  return Math.min(n, MAX_LIMIT);
}

/** The offset of the deepest page the API serves at this page size, under maxOffset. */
export function lastReachableOffset(limit: number, maxOffset = MAX_OFFSET): number {
  return Math.floor(maxOffset / limit) * limit;
}

/**
 * The offset from a query value: 0 when missing, malformed or negative, and the last reachable
 * page's offset when it's past maxOffset (see maxOffsetFor).
 */
export function parseOffset(raw: string | undefined, limit: number, maxOffset = MAX_OFFSET): number {
  const n = parseNonNegativeInt(raw);
  if (n === undefined) return 0;
  return n > maxOffset ? lastReachableOffset(limit, maxOffset) : n;
}

/** How many pages the list has, and how many of them the API lets a reader reach. */
export function pageCount(
  total: number,
  limit: number,
  maxOffset = MAX_OFFSET
): { pages: number; reachable: number } {
  const pages = Math.ceil(total / limit);
  return { pages, reachable: Math.min(pages, lastReachableOffset(limit, maxOffset) / limit + 1) };
}
