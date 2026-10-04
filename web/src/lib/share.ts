// Share cards (#88, design docs/design/88-share-cards.md). A share URL carries only what its card
// prints: a member ID and two counts, a bill ID, the sharer's vote and an optional member ID, or
// (aggregate cards, #166) a bill ID and the scope of one published cell from #89.
// The share pages, their images and the share dialog all use these functions, so the URL grammar
// and the card's words live in one place.

import { scopeName } from "./aggregates";
import { DELEGATE_AREAS, RESIDENT_COMMISSIONER_AREAS } from "./districts";
import { ordinal } from "./graph";
import { BILL_TYPE_LABELS, type AggregateCell } from "./types";
import { formatDate } from "./utils";

/** A scorecard needs at least this many compared bills before it can be shared. */
export const MIN_SHARE_COMPARED = 5;
/** No member has recorded positions on more bills than this, so larger counts are refused. */
export const MAX_SHARE_COMPARED = 2000;

export const SHARE_IMAGE_WIDTH = 1200;
export const SHARE_IMAGE_HEIGHT = 630;

/** The Methodology page (#75) a card points to: printed in the image footer, linked from share pages. */
export const SHARE_METHODOLOGY_PATH = "/methodology";

/** Longest bill title printed on a card, so it fits in two lines. */
export const MAX_CARD_TITLE_LENGTH = 110;

export type ShareVote = "yea" | "nay";

export interface RepShare {
  memberId: string;
  matching: number;
  compared: number;
}

export interface BillShare {
  billId: string;
  vote: ShareVote;
  memberId?: string;
}

/** One published cell of #89's aggregates: `scopeKey` is "" (national), "CA" or "CA-12". */
export interface AggregateShare {
  billId: string;
  scopeKey: string;
}

export interface ParsedBillId {
  billType: string;
  congress: number;
  number: number;
}

const MEMBER_ID = /^[A-Z][0-9]{6}$/;
// Canonical forms only (lowercase, no leading zeros), so each card has exactly one URL.
const BILL_ID = /^([a-z]+)-([1-9][0-9]{1,2})-([1-9][0-9]{0,4})$/;
const SCORE = /^(0|[1-9][0-9]{0,3})-of-([1-9][0-9]{0,3})$/;
// The aggregation job's scope keys: a state ("CA") or a district ("CA-12", at-large "AK-0").
const SCOPE_KEY = /^[A-Z]{2}(-(0|[1-9][0-9]?))?$/;

/** The URL segment of the national cell, whose scope key is "" (an empty segment can't be routed). */
export const NATIONAL_SCOPE_SEGMENT = "national";

export function isMemberId(value: string): boolean {
  return MEMBER_ID.test(value);
}

/** Splits "hr-119-1" into its parts, or returns null for a non-canonical or unknown bill ID. */
export function parseBillId(billId: string): ParsedBillId | null {
  const m = BILL_ID.exec(billId);
  if (!m || !Object.hasOwn(BILL_TYPE_LABELS, m[1])) return null;
  return { billType: m[1], congress: Number(m[2]), number: Number(m[3]) };
}

/** Parses the `[member]/[score]` segments of /share/rep/..., or null if they aren't canonical. */
export function parseRepShare(member: string, score: string): RepShare | null {
  if (!isMemberId(member)) return null;
  const m = SCORE.exec(score);
  if (!m) return null;
  const matching = Number(m[1]);
  const compared = Number(m[2]);
  if (matching > compared || compared < MIN_SHARE_COMPARED || compared > MAX_SHARE_COMPARED) {
    return null;
  }
  return { memberId: member, matching, compared };
}

/** Parses the `[bill]/[vote]/[member]?` segments of /share/bill/..., or null. */
export function parseBillShare(bill: string, vote: string, member?: string): BillShare | null {
  if (!parseBillId(bill)) return null;
  if (vote !== "yea" && vote !== "nay") return null;
  if (member === undefined) return { billId: bill, vote };
  if (!isMemberId(member)) return null;
  return { billId: bill, vote, memberId: member };
}

/** Parses the `[bill]/[scope]` segments of /share/aggregate/..., or null if they aren't canonical. */
export function parseAggregateShare(bill: string, scope: string): AggregateShare | null {
  if (!parseBillId(bill)) return null;
  if (scope === NATIONAL_SCOPE_SEGMENT) return { billId: bill, scopeKey: "" };
  return SCOPE_KEY.test(scope) ? { billId: bill, scopeKey: scope } : null;
}

export function repShareUrl(share: RepShare): string {
  return `/share/rep/${share.memberId}/${share.matching}-of-${share.compared}`;
}

export function billShareUrl(share: BillShare): string {
  const base = `/share/bill/${share.billId}/${share.vote}`;
  return share.memberId ? `${base}/${share.memberId}` : base;
}

export function aggregateShareUrl(share: AggregateShare): string {
  return `/share/aggregate/${share.billId}/${share.scopeKey || NATIONAL_SCOPE_SEGMENT}`;
}

/** The card image for a share page URL. */
export function shareImageUrl(pageUrl: string): string {
  return `${pageUrl}/image.png`;
}

// ---------------------------------------------------------------------------
// Card copy
// ---------------------------------------------------------------------------

/** What a card needs to know about a member, from public data. */
export interface CardMember {
  firstName: string;
  lastName: string;
  chamber: string;
  state: string;
  district?: number;
  party: string;
}

/**
 * A member's recorded position on a bill: "yea", "nay", "present", "not_voting", or null when
 * there's no roll call for them on it (a voice vote, not voted yet, or not in office).
 */
export type MemberPositionVote = "yea" | "nay" | "present" | "not_voting" | null;

/** One run of text on the card; `strong` runs are bold. */
export interface CardText {
  text: string;
  strong?: boolean;
}

export interface CardCopy {
  /** A one-line summary for page titles and link previews. */
  summary: string;
  /** The small line above the headline, e.g. "Scorecard" or "H.R. 1 · 119th Congress". */
  eyebrow: string;
  /** Bill title for bill cards, already shortened to fit. */
  title?: string;
  /** The headline, as runs so the names and numbers can be bold. */
  headline: CardText[];
  /** A second statement under the headline (the member's vote on a bill card). */
  detail?: CardText[];
  /** Small print that says what the numbers are. */
  note: string;
  /** The same words as one plain string, for alt text and descriptions. */
  plain: string;
  /** The Methodology link printed in the image footer; the scorecard section when unset. */
  methodology?: { label: string; path: string };
}

const PRINTED_PARTIES = new Set(["D", "R", "I"]);

export function memberTitle(member: CardMember): string {
  if (member.chamber === "Senate") return "Sen.";
  if (DELEGATE_AREAS.has(member.state)) return "Del.";
  if (RESIDENT_COMMISSIONER_AREAS.has(member.state)) return "Res. Comm.";
  return "Rep.";
}

/** "D-NY" for a senator, "R-TX-2" for a representative, "R-AK-AL" at large, "D-DC" for a delegate. */
export function memberSeat(member: CardMember): string {
  const party = PRINTED_PARTIES.has(member.party) ? `${member.party}-` : "";
  if (member.chamber === "Senate") return `${party}${member.state}`;
  if (DELEGATE_AREAS.has(member.state) || RESIDENT_COMMISSIONER_AREAS.has(member.state)) {
    return `${party}${member.state}`;
  }
  const district = member.district ? String(member.district) : "AL";
  return `${party}${member.state}-${district}`;
}

/** "Sen. Jane Doe (D-NY)". */
export function memberLabel(member: CardMember): string {
  return `${memberTitle(member)} ${member.firstName} ${member.lastName} (${memberSeat(member)})`;
}

/** "Sen. Doe". */
export function memberShortLabel(member: CardMember): string {
  return `${memberTitle(member)} ${member.lastName}`;
}

/** The same rounding as #72's score(): round(100 × matching / compared). */
export function alignmentPercent(matching: number, compared: number): number {
  return Math.round((100 * matching) / compared);
}

/** Shortens a title to `max` characters at a word boundary, with an ellipsis. */
export function truncateTitle(title: string, max = MAX_CARD_TITLE_LENGTH): string {
  const clean = title.replace(/\s+/g, " ").trim();
  if (clean.length <= max) return clean;
  const cut = clean.slice(0, max - 1);
  const space = cut.lastIndexOf(" ");
  const base = space > max / 2 ? cut.slice(0, space) : cut;
  return `${base.replace(/[\s,;:.-]+$/, "")}…`;
}

function voteWord(vote: ShareVote): string {
  return vote === "yea" ? "Yea" : "Nay";
}

export function plainText(runs: CardText[]): string {
  return runs.map((r) => r.text).join("");
}

/** Joins parts into sentences, adding a period only where a part doesn't already end one. */
function sentences(parts: string[]): string {
  return parts
    .filter(Boolean)
    .map((p) => (/[.!?…]$/.test(p) ? p : `${p}.`))
    .join(" ");
}

export function repCardCopy(member: CardMember, share: RepShare): CardCopy {
  const pct = alignmentPercent(share.matching, share.compared);
  const headline: CardText[] = [
    { text: "I agree with " },
    { text: memberLabel(member), strong: true },
    { text: " on " },
    { text: `${share.matching} of ${share.compared}`, strong: true },
    { text: ` bills (${pct}%)` },
  ];
  const note =
    `My votes on Just a Bill compared with ${memberShortLabel(member)}'s recorded votes ` +
    "on final passage";
  return {
    summary: plainText(headline),
    eyebrow: "Scorecard",
    headline,
    note,
    plain: sentences([plainText(headline), note]),
  };
}

export interface BillCardInput {
  billType: string;
  number: number;
  congress: number;
  title: string;
}

function memberVoteDetail(member: CardMember, position: MemberPositionVote): CardText[] {
  const who: CardText = { text: memberLabel(member), strong: true };
  switch (position) {
    case "yea":
    case "nay":
      return [who, { text: " voted " }, { text: voteWord(position), strong: true }];
    case "present":
      return [who, { text: " voted " }, { text: "Present", strong: true }];
    case "not_voting":
      return [who, { text: " did not vote" }];
    default:
      return [who, { text: " has no recorded vote on this bill" }];
  }
}

/** "H.R. 1", "H.R. 1 · 119th Congress" and the shortened title, for the cards about one bill. */
function billHeading(bill: BillCardInput): { label: string; eyebrow: string; title: string } {
  const type = BILL_TYPE_LABELS[bill.billType] ?? bill.billType.toUpperCase();
  const label = `${type} ${bill.number}`;
  return { label, eyebrow: `${label} · ${ordinal(bill.congress)} Congress`, title: truncateTitle(bill.title) };
}

export function billCardCopy(
  bill: BillCardInput,
  vote: ShareVote,
  member?: { member: CardMember; position: MemberPositionVote }
): CardCopy {
  const { label, eyebrow, title } = billHeading(bill);
  const headline: CardText[] = [{ text: "I'd vote " }, { text: voteWord(vote), strong: true }];
  const detail = member ? memberVoteDetail(member.member, member.position) : undefined;
  const note = member
    ? "My vote on Just a Bill. The member's vote is from the congressional record."
    : "My vote on Just a Bill. How would you vote?";
  return {
    summary: `I'd vote ${voteWord(vote)} on ${label}`,
    eyebrow,
    title,
    headline,
    detail,
    note,
    plain: sentences([eyebrow, title, plainText(headline), detail ? plainText(detail) : ""]),
  };
}

/**
 * The label #89 requires on every aggregate card, printed on the image itself, so a screenshot or
 * a cropped preview still says what the numbers are.
 */
export const AGGREGATE_CARD_LABEL = "Just a Bill users, not a poll";

/** The Methodology section an aggregate card points to (the same as METHODOLOGY_AGGREGATES). */
export const AGGREGATE_METHODOLOGY_PATH = `${SHARE_METHODOLOGY_PATH}#aggregates`;

/** A cell's place in a sentence: "nationwide", "in CA", "in CA-12" or "in WY at-large". */
export function aggregatePlace(scopeKey: string): string {
  return scopeKey === "" ? "nationwide" : `in ${scopeName(scopeKey)}`;
}

/** A served cell with its numbers. */
export type PublishedCell = AggregateCell & { yea_pct: number; nay_pct: number };

/** Whether a served cell may be printed on a card: published with its numbers, never held. */
export function isShareableCell(cell: AggregateCell | null): cell is PublishedCell {
  return cell !== null && cell.status === "published" && cell.yea_pct !== null && cell.nay_pct !== null;
}

/**
 * The aggregate card: how Just a Bill users in one scope voted on a bill, with the "not a poll"
 * label. Every number comes from the published cell, never from the URL.
 */
export function aggregateCardCopy(bill: BillCardInput, cell: PublishedCell): CardCopy {
  const { label, eyebrow, title } = billHeading(bill);
  const place = aggregatePlace(cell.scope_key);
  const headline: CardText[] = [
    { text: `Just a Bill users ${place}: ` },
    // The image lays out one box per word, so a no-break space keeps "62% Yea" on one line, and
    // the comma stays in the bold run rather than print apart from "Yea".
    { text: `${cell.yea_pct}%\u00a0Yea, `, strong: true },
    { text: `${cell.nay_pct}%\u00a0Nay`, strong: true },
  ];
  const detail: CardText[] = [];
  if (cell.voters_floor !== null) {
    detail.push({ text: `${cell.voters_floor.toLocaleString("en-US")}+ users`, strong: true });
  }
  if (cell.published_at) {
    detail.push({ text: `${detail.length > 0 ? " · " : ""}published ${formatDate(cell.published_at)}` });
  }
  const note = `${AGGREGATE_CARD_LABEL}. Opt-in users who chose to vote, not a sample of residents.`;
  return {
    summary: `Just a Bill users ${place}: ${cell.yea_pct}% Yea, ${cell.nay_pct}% Nay on ${label}`,
    eyebrow,
    title,
    headline,
    detail: detail.length > 0 ? detail : undefined,
    note,
    plain: sentences([eyebrow, title, plainText(headline), detail.length > 0 ? plainText(detail) : "", note]),
    methodology: { label: "How these numbers work", path: AGGREGATE_METHODOLOGY_PATH },
  };
}

/** Maps an API position vote ("Yea", "Aye", "No", "Not Voting", ...) to a card position. */
export function normalizePositionVote(vote: string | undefined): MemberPositionVote {
  switch ((vote ?? "").trim().toLowerCase().replace(/[\s-]+/g, "_")) {
    case "yea":
    case "aye":
    case "yes":
      return "yea";
    case "nay":
    case "no":
      return "nay";
    case "present":
      return "present";
    case "not_voting":
      return "not_voting";
    default:
      return null;
  }
}
