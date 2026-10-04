// The My votes page's list (#726, #738, redesigned in #843): which votes the filters and the search
// show, in what order, how many each option holds, and the summary above them. Pure, so the page
// and its tests share one definition.

import { BILL_VIEWS } from "@/lib/bill-views";
import type { LocalVote, LocalVotes } from "@/lib/local/votes";
import { parseBillId } from "@/lib/share";
import { billLabel } from "@/lib/share-links";
import type { BillStatus, UserVoteChoice } from "@/lib/types";

/** Letters and digits only, lowercased: "H.R. 1" and "hr 1" both become "hr1". */
function compact(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9]/g, "");
}

/**
 * Whether a vote's bill matches what was typed in the search box: its title contains it (case
 * aside), or its number starts with it ("hr 1" finds H.R. 1 and H.R. 12). An empty search matches all.
 */
export function matchesSearch(billId: string, vote: LocalVote, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  if (vote.title?.toLowerCase().includes(q)) return true;
  const typed = compact(q);
  return typed.length > 0 && compact(billLabel(billId)).startsWith(typed);
}

/** Rows a page: a row is one line on a wide screen, two or three on a phone. */
export const MY_VOTES_PAGE_SIZE = 25;

/** Below this many votes the filter bar is left out: the summary's figures filter a short list. */
export const MY_VOTES_FILTER_BAR_MIN = 10;

/**
 * Each congress's bills that passed a chamber or went further, with their status (`GET
 * /bill-statuses`, #853), joined to the votes in the browser so the server never learns which
 * bills anyone voted on.
 */
export interface BillStatuses {
  /** The status of each listed bill, by bill ID. */
  byId: Readonly<Record<string, BillStatus>>;
  /** The congresses whose list loaded: one of their bills that isn't listed hasn't passed a chamber. */
  congresses: ReadonlySet<number>;
}

/** No list loaded: every bill's status is unknown. */
export const NO_BILL_STATUSES: BillStatuses = { byId: {}, congresses: new Set() };

/**
 * Where a bill stands, as far as the lists say: its status when it's listed, `not_passed` when its
 * congress's list loaded without it, and undefined when that list didn't load. Nothing is guessed.
 */
export type VotedBillStatus = BillStatus | "not_passed" | undefined;

export function votedBillStatus(statuses: BillStatuses, billId: string): VotedBillStatus {
  const status = statuses.byId[billId];
  if (status) return status;
  const congress = parseBillId(billId)?.congress;
  return congress !== undefined && statuses.congresses.has(congress) ? "not_passed" : undefined;
}

/** The status groups the page filters by: /bills' Laws and Passed a chamber, and the rest. */
export type MyVotesStatusGroup = "laws" | "passed" | "not_passed" | "unknown";

/** The groups in the order the page lists them: furthest along first. */
export const MY_VOTES_STATUS_GROUPS: readonly MyVotesStatusGroup[] = ["laws", "passed", "not_passed", "unknown"];

export const MY_VOTES_STATUS_GROUP_LABELS: Record<MyVotesStatusGroup, string> = {
  laws: "Became law",
  passed: "Passed a chamber",
  not_passed: "Not passed",
  unknown: "Status unavailable",
};

/**
 * The group a bill falls in. The first two match /bills' Laws and Passed a chamber views (BILL_VIEWS);
 * a bill not listed (introduced, in committee or reported) hasn't passed. Unknown is never guessed.
 */
export function statusGroup(statuses: BillStatuses, billId: string): MyVotesStatusGroup {
  const status = votedBillStatus(statuses, billId);
  if (status === undefined) return "unknown";
  if (status === "not_passed") return "not_passed";
  const view = BILL_VIEWS.find((v) => v.statuses.includes(status));
  if (view?.key === "laws" || view?.key === "passed") return view.key;
  return "not_passed";
}

export interface MyVotesQuery {
  vote: UserVoteChoice | "all";
  status: MyVotesStatusGroup | "all";
  /** A congress number, or null for every congress. */
  congress: number | null;
  search: string;
}

export const MY_VOTES_ALL: MyVotesQuery = { vote: "all", status: "all", congress: null, search: "" };

/** How many of the query's filters narrow the list (the search aside). */
export function activeFilterCount(q: MyVotesQuery): number {
  return Number(q.vote !== "all") + Number(q.status !== "all") + Number(q.congress !== null);
}

export function matchesQuery(billId: string, vote: LocalVote, statuses: BillStatuses, q: MyVotesQuery): boolean {
  if (q.vote !== "all" && vote.vote !== q.vote) return false;
  if (q.status !== "all" && statusGroup(statuses, billId) !== q.status) return false;
  if (q.congress !== null && parseBillId(billId)?.congress !== q.congress) return false;
  return matchesSearch(billId, vote, q.search);
}

/** The bills a query shows, newest vote first (ties by bill ID, so the order is stable). */
export function myVoteIds(votes: LocalVotes, statuses: BillStatuses, q: MyVotesQuery): string[] {
  return Object.entries(votes)
    .filter(([id, v]) => matchesQuery(id, v, statuses, q))
    .sort(([a, va], [b, vb]) => Date.parse(vb.at) - Date.parse(va.at) || a.localeCompare(b))
    .map(([id]) => id);
}

/**
 * The rows to show (#738): the view's bills in the order they had when the query last changed, so
 * a row whose vote changes keeps its place (even under a filter it no longer matches), with any
 * bill voted on since (in another tab, or by an import) that matches the query first. A row leaves
 * when its vote is gone, unless this page removed it (`removed`), where it stays to offer Undo.
 */
export function myVoteRows(
  view: readonly string[],
  votes: LocalVotes,
  statuses: BillStatuses,
  q: MyVotesQuery,
  removed: Readonly<Record<string, LocalVote>>,
): string[] {
  const inView = new Set(view);
  const fresh = myVoteIds(votes, statuses, q).filter((id) => !inView.has(id));
  return [...fresh, ...view.filter((id) => id in votes || id in removed)];
}

export interface MyVoteSummary {
  total: number;
  yea: number;
  nay: number;
  skip: number;
  /** How many of the voted-on bills became law, or null while any of their statuses is unknown. */
  becameLaw: number | null;
}

export function myVoteSummary(votes: LocalVotes, statuses: BillStatuses): MyVoteSummary {
  const s = { total: 0, yea: 0, nay: 0, skip: 0 };
  let becameLaw: number | null = 0;
  for (const [id, v] of Object.entries(votes)) {
    s.total++;
    s[v.vote]++;
    const group = statusGroup(statuses, id);
    if (group === "unknown") becameLaw = null;
    else if (group === "laws" && becameLaw !== null) becameLaw++;
  }
  return { ...s, becameLaw };
}

export interface MyVoteFacets {
  vote: Record<UserVoteChoice, number>;
  status: Record<MyVotesStatusGroup, number>;
  /** Votes per congress, for every congress the visitor voted in. */
  congress: Record<number, number>;
}

/**
 * How many votes each filter option would show, given the search and the other two filters: the
 * number next to "Yea" is what picking Yea lists.
 */
export function myVoteFacets(votes: LocalVotes, statuses: BillStatuses, q: MyVotesQuery): MyVoteFacets {
  const facets: MyVoteFacets = {
    vote: { yea: 0, nay: 0, skip: 0 },
    status: { laws: 0, passed: 0, not_passed: 0, unknown: 0 },
    congress: {},
  };
  for (const [id, v] of Object.entries(votes)) {
    const congress = parseBillId(id)?.congress;
    // Every congress voted in is listed, even one the other filters empty.
    if (congress !== undefined) facets.congress[congress] ??= 0;
    if (matchesQuery(id, v, statuses, { ...q, vote: "all" })) facets.vote[v.vote]++;
    if (matchesQuery(id, v, statuses, { ...q, status: "all" })) facets.status[statusGroup(statuses, id)]++;
    if (congress !== undefined && matchesQuery(id, v, statuses, { ...q, congress: null })) facets.congress[congress]++;
  }
  return facets;
}
