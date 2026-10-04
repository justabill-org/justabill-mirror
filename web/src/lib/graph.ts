// Helpers for the civic_graph panels: related bills, companion votes and "Works most with".

import type { CompanionMemberVote, GraphRelatedBill, MemberTerm, RelatedBill } from "./types";
import { BILL_TYPE_LABELS } from "./types";

/** "H.R. 1" for bill type "hr" and number 1. */
export function billLabel(billType: string, number: number): string {
  const type = BILL_TYPE_LABELS[billType] ?? billType.toUpperCase();
  return `${type} ${number}`;
}

/** "S. 1" for bill ID "s-119-1"; the ID itself when it isn't <type>-<congress>-<number>. */
export function billLabelFromId(billId: string): string {
  const [billType, congress, number] = billId.split("-");
  if (!billType || !congress || !number || !/^\d+$/.test(number)) return billId;
  return billLabel(billType, Number(number));
}

/** Vote counts for one party on a roll call. `other` holds values that aren't the four below. */
export interface PartyTally {
  party: string;
  yea: number;
  nay: number;
  present: number;
  notVoting: number;
  other: number;
}

/** The label a tally uses for members whose party isn't known. */
export const UNKNOWN_PARTY = "Unknown";

const PARTY_ORDER = ["D", "R", "I", "ID"];

/**
 * Counts a roll call's votes by party: Democrats, Republicans and independents first, then any
 * other party alphabetically, then members without a known party.
 */
export function tallyByParty(votes: CompanionMemberVote[]): PartyTally[] {
  const byParty = new Map<string, PartyTally>();
  for (const v of votes) {
    const party = v.party || UNKNOWN_PARTY;
    let tally = byParty.get(party);
    if (!tally) {
      tally = { party, yea: 0, nay: 0, present: 0, notVoting: 0, other: 0 };
      byParty.set(party, tally);
    }
    switch (v.vote) {
      case "Yea":
        tally.yea++;
        break;
      case "Nay":
        tally.nay++;
        break;
      case "Present":
        tally.present++;
        break;
      case "Not Voting":
        tally.notVoting++;
        break;
      default:
        tally.other++;
    }
  }
  const rank = (party: string) => {
    if (party === UNKNOWN_PARTY) return PARTY_ORDER.length + 1;
    const i = PARTY_ORDER.indexOf(party);
    return i === -1 ? PARTY_ORDER.length : i;
  };
  return [...byParty.values()].sort((a, b) => rank(a.party) - rank(b.party) || a.party.localeCompare(b.party));
}

/**
 * Converts the related bills stored on the bill row (Congress.gov JSON) to the graph shape.
 * The bill page uses it when the graph endpoint fails or has nothing for the bill, e.g. before
 * backfill-links has written the link tables.
 */
export function relatedBillsFromJSON(related: RelatedBill[] | undefined): GraphRelatedBill[] {
  const byId = new Map<string, GraphRelatedBill>();
  for (const r of related ?? []) {
    const billType = r.type.toLowerCase().replaceAll(".", "");
    const id = `${billType}-${r.congress}-${r.number}`;
    const entry = byId.get(id) ?? {
      bill_id: id,
      congress: r.congress,
      bill_type: billType,
      number: r.number,
      title: r.title,
      relation_types: [],
      shared_subjects: 0,
    };
    for (const detail of r.relationshipDetails ?? []) {
      if (detail.type && !entry.relation_types.includes(detail.type)) {
        entry.relation_types.push(detail.type);
      }
    }
    byId.set(id, entry);
  }
  return [...byId.values()];
}

/** "119th", "121st", "112th". */
export function ordinal(n: number): string {
  const mod100 = n % 100;
  if (mod100 >= 11 && mod100 <= 13) return `${n}th`;
  switch (n % 10) {
    case 1:
      return `${n}st`;
    case 2:
      return `${n}nd`;
    case 3:
      return `${n}rd`;
    default:
      return `${n}th`;
  }
}

/**
 * The member's most recent term, or undefined if they have none: the latest congress's, and of a
 * congress in which they sat in both chambers, the term that didn't end early (no end_date).
 */
export function latestTerm(terms: MemberTerm[]): MemberTerm | undefined {
  let latest: MemberTerm | undefined;
  for (const term of terms) {
    if (!latest || compareTermsNewestFirst(term, latest) < 0) latest = term;
  }
  return latest;
}

/**
 * Orders terms newest first: by congress, then within one congress the term without an end_date
 * (the seat still held) before the one that ended, then the later end_date first.
 */
export function compareTermsNewestFirst(a: MemberTerm, b: MemberTerm): number {
  if (a.congress !== b.congress) return b.congress - a.congress;
  if (!a.end_date || !b.end_date) return (a.end_date ? 1 : 0) - (b.end_date ? 1 : 0);
  return b.end_date.localeCompare(a.end_date);
}

/** The year the 1st Congress first met; each congress sits for two years. */
const FIRST_CONGRESS_YEAR = 1789;

/** The years a congress sits: 2025 to 2027 for the 119th (it ends on January 3, 2027). */
export function congressYears(congress: number): { start: number; end: number } {
  const start = FIRST_CONGRESS_YEAR + 2 * (congress - 1);
  return { start, end: start + 2 };
}

/**
 * A term's years: its congress's, from the year of start_date when the record has one (a member
 * sworn in after a special election) to the year of end_date when they left early.
 */
export function termYears(term: MemberTerm): { start: number; end: number } {
  const years = congressYears(term.congress);
  return { start: utcYear(term.start_date) ?? years.start, end: utcYear(term.end_date) ?? years.end };
}

/** "2025–2027", or "2026" for a term that began and ended in one year. */
export function termYearsLabel(term: MemberTerm): string {
  const { start, end } = termYears(term);
  return start === end ? `${start}` : `${start}–${end}`;
}

function utcYear(date: string | undefined): number | undefined {
  if (!date) return undefined;
  const ms = Date.parse(date);
  return Number.isNaN(ms) ? undefined : new Date(ms).getUTCFullYear();
}
