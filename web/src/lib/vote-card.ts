import type { SponsorEntry } from "./sponsors";
import type { BillCardFacts, BillSummary, CardEnactment, CardPassage } from "./types";

// What a /vote card and its Details popup say about a bill (#662, #717), from
// `GET /bills?include=summary,card`: the API picks the final votes (db/scoring's final-passage
// allowlist), the CRS lead and the law number, and these helpers only word them.

/** How many law changes the Details popup lists, so it stays short on a phone; the bill page has the rest. */
export const MAX_LAW_CHANGES = 3;

/** What the card leads with: the AI summary's short summary, or else the CRS summary's lead. */
export type CardLead = { source: "ai"; text: string } | { source: "crs"; text: string };

/** The card's lead, labeled by source, or null when the bill has neither summary. */
export function cardLead(
  summary: BillSummary | null | undefined,
  card: BillCardFacts | null | undefined
): CardLead | null {
  if (summary?.short_summary) return { source: "ai", text: summary.short_summary };
  if (card?.crs?.lead) return { source: "crs", text: card.crs.lead };
  return null;
}

/** Who the bill affects, from the AI summary. */
export function whoItAffects(summary: BillSummary | null | undefined): string | undefined {
  return summary?.who_it_affects;
}

/** "House" from the stored "house" or "House". */
export function chamberName(chamber: string): string {
  return chamber.charAt(0).toUpperCase() + chamber.slice(1).toLowerCase();
}

/**
 * Whether the vote went against the bill. The card's votes are each chamber's latest final vote,
 * which can be one that failed: "Failed", "Motion Rejected", "Veto Sustained" and the like.
 */
export function passageFailed(p: CardPassage): boolean {
  return /fail|reject|not agreed|sustained/i.test(p.result ?? "");
}

const UNRECORDED: Record<Exclude<CardPassage["method"], "roll">, string> = {
  voice: "voice vote",
  uc: "unanimous consent",
};

/** One chamber's vote in a few words: "413–0", "failed 190–230", "voice vote" or "unanimous consent". */
export function passageShort(p: CardPassage): string {
  let how = "roll call";
  if (p.method !== "roll") how = UNRECORDED[p.method];
  else if (p.yeas != null && p.nays != null) how = `${p.yeas}–${p.nays}`;
  return passageFailed(p) ? `failed ${how}` : how;
}

/** "Public Law 119-62" (or "Private Law …"), or undefined when the clerk's action didn't name it. */
export function lawName(enacted: CardEnactment | null | undefined): string | undefined {
  if (!enacted?.law_number) return undefined;
  return `${enacted.law_type === "private" ? "Private" : "Public"} Law ${enacted.law_number}`;
}

/** How many of a bill's cosponsors sit with one party. */
export interface PartyCount {
  party: string;
  count: number;
}

const PARTY_NAMES: Record<string, [string, string]> = {
  D: ["Democrat", "Democrats"],
  R: ["Republican", "Republicans"],
  I: ["independent", "independents"],
  ID: ["independent", "independents"],
};

/** "12 Democrats", "1 Republican"; a party we have no name for keeps its code. */
export function partyCountLabel({ party, count }: PartyCount): string {
  const names = PARTY_NAMES[party];
  if (!names) return `${count} ${party}`;
  return `${count} ${count === 1 ? names[0] : names[1]}`;
}

/**
 * The cosponsors by party, in A-to-Z order of the party code whatever the counts, so the order
 * never says which side matters more. Cosponsors with no party on record are left out.
 */
export function cosponsorParties(cosponsors: readonly SponsorEntry[]): PartyCount[] {
  const counts = new Map<string, number>();
  for (const c of cosponsors) {
    if (c.party) counts.set(c.party, (counts.get(c.party) ?? 0) + 1);
  }
  return [...counts.entries()]
    .map(([party, count]) => ({ party, count }))
    .sort((x, y) => x.party.localeCompare(y.party));
}
