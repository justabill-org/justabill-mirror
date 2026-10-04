// Helpers for the published user-vote aggregates (docs/design/89-aggregate-analytics.md, plan item
// 8). A plain module, so server and client components can both import it.

import { districtName } from "./districts";
import type { LocalRep } from "./local/reps";
import type { AggregateCell, BillAggregatesResponse, RepAlignment } from "./types";

/** The label every aggregate panel carries (design #89, "Web"); it links to METHODOLOGY_AGGREGATES. */
export const AGGREGATES_LABEL =
  "Opt-in Just a Bill users who say they live here. Not a poll. Updated hourly; new accounts count after 48 hours.";

/** The Methodology section with the publication rules. */
export const METHODOLOGY_AGGREGATES = "/methodology#aggregates";

/** A state and, for a House seat, its district (0 for an at-large seat or a delegate). */
export interface Constituency {
  state: string;
  district: number | null;
}

/** The scope key of a district ("CA-12", "AK-0"), matching the aggregation job's keys. */
export function districtKey(state: string, district: number): string {
  return `${state}-${district}`;
}

/** "Nationwide" for the national cell, "CA" for a state, "CA-12" or "WY at-large" for a district. */
export function scopeName(scopeKey: string): string {
  if (scopeKey === "") return "Nationwide";
  const [state, district] = scopeKey.split("-");
  return district === undefined ? state : districtName(state, Number(district));
}

/**
 * The visitor's own constituency: the signed-in account's district, which is the one their votes
 * count in, or else the representatives saved in this browser. Null when neither is known.
 */
export function ownConstituency(
  account: { state?: string; district?: number } | null,
  local: Constituency | null,
): Constituency | null {
  if (account?.state) return { state: account.state, district: account.district ?? null };
  return local?.state ? { state: local.state, district: local.district } : null;
}

/** The scope keys of a constituency, district first, then the state. */
export function constituencyKeys(c: Constituency | null): string[] {
  if (!c) return [];
  return c.district === null ? [c.state] : [districtKey(c.state, c.district), c.state];
}

/** The served state and district cells, for the picker: states, then districts, by key. */
export function pickerCells(data: BillAggregatesResponse): AggregateCell[] {
  return [...data.states, ...data.districts];
}

/** The cell for a scope key in the bill's served cells, or null when it isn't served. */
export function findCell(data: BillAggregatesResponse, scopeKey: string): AggregateCell | null {
  return pickerCells(data).find((c) => c.scope_key === scopeKey) ?? null;
}

/** "340+ users": the API rounds the count down to a multiple of 10. */
export function votersLabel(floor: number | null): string | null {
  return floor === null ? null : `${floor.toLocaleString("en-US")}+ users`;
}

/** The scope key a member's alignment is measured in: the state for a senator, else the district. */
export function repScopeKey(rep: LocalRep): string | null {
  if (rep.chamber === "Senate") return rep.state;
  return rep.district === undefined || rep.district === null ? null : districtKey(rep.state, rep.district);
}

/**
 * The member's alignment with users in the visitor's constituency: the newest congress with a
 * comparison, limited to `congresses` when the scorecard's switch picked some. Null when there's
 * nothing to show.
 */
export function pickAlignment(
  rows: readonly RepAlignment[],
  rep: LocalRep,
  congresses: readonly number[] = [],
): RepAlignment | null {
  const key = repScopeKey(rep);
  if (!key) return null;
  const matching = rows.filter(
    (r) => r.scope_key === key && r.bills_compared > 0 && (congresses.length === 0 || congresses.includes(r.congress)),
  );
  return matching.reduce<RepAlignment | null>((best, r) => (best && best.congress >= r.congress ? best : r), null);
}
