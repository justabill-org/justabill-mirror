// Loads what a share card prints from public API data (#88). Everything about a member, a bill or
// an aggregate comes from here, never from the share URL. The loaders return null when the card
// must 404: an unknown bill or member, or an aggregate cell that isn't published.

import { findCell, METHODOLOGY_AGGREGATES } from "./aggregates";
import { ApiError, getBill, getBillAggregates, getMember, getMemberPositions } from "./api";
import { latestTerm } from "./graph";
import {
  aggregateCardCopy,
  billCardCopy,
  isShareableCell,
  normalizePositionVote,
  parseBillId,
  type AggregateShare,
  type BillShare,
  type CardCopy,
  type CardMember,
} from "./share";
import type { MemberDetail } from "./types";

export interface ShareCardData {
  copy: CardCopy;
  /** Where the card's call to action leads. */
  actionHref: string;
  actionLabel: string;
  /** The member's page, when the card names one. */
  memberHref?: string;
  memberLinkLabel?: string;
  /** The "What is this?" line under the card; the share page has a default for vote cards. */
  about?: string;
  /** Where the page's Methodology link goes, and its words. */
  methodology?: { href: string; label: string };
}

/** Resolves to null when the API answers 404 or 400; other errors propagate (a 500, not a cached 404). */
async function orNotFound<T>(promise: Promise<T>): Promise<T | null> {
  try {
    return await promise;
  } catch (err) {
    if (err instanceof ApiError && (err.status === 404 || err.status === 400)) return null;
    throw err;
  }
}

/** The member as the card prints them: their term in `congress` if they served then, else their latest. */
function cardMember(detail: MemberDetail, congress?: number): CardMember | null {
  const terms = detail.terms ?? [];
  const term = terms.find((t) => t.congress === congress) ?? latestTerm(terms);
  if (!term) return null;
  return {
    firstName: detail.first_name,
    lastName: detail.last_name,
    chamber: term.chamber,
    state: term.state,
    district: term.district,
    party: term.party,
  };
}

/**
 * The bill card. The member's vote is looked up from their positions in the bill's congress; the
 * URL never carries it. If the positions can't be loaded the card 404s rather than claim "no
 * recorded vote".
 */
export async function loadBillCard(share: BillShare): Promise<ShareCardData | null> {
  const parsed = parseBillId(share.billId);
  if (!parsed) return null;
  const response = await orNotFound(getBill(share.billId));
  if (!response?.bill) return null;
  const { bill } = response;
  const input = { billType: bill.bill_type, number: bill.number, congress: bill.congress, title: bill.title };
  const base = { actionHref: `/bills/${share.billId}`, actionLabel: "How would you vote?" };

  if (!share.memberId) return { ...base, copy: billCardCopy(input, share.vote) };

  const detail = await orNotFound(getMember(share.memberId));
  if (!detail) return null;
  const member = cardMember(detail, bill.congress);
  if (!member) return null;
  const positions = await orNotFound(getMemberPositions(share.memberId, bill.congress));
  if (!positions) return null;
  const position = (positions.positions ?? []).find((p) => p.bill_id === share.billId);

  return {
    ...base,
    copy: billCardCopy(input, share.vote, { member, position: normalizePositionVote(position?.vote) }),
    memberHref: `/members/${share.memberId}`,
    memberLinkLabel: `See ${member.firstName} ${member.lastName}'s votes`,
  };
}

/**
 * The aggregate card (#166): one cell of #89's published aggregates. Suppressed cells aren't
 * served and held ones are under review, so both 404, as does every bill while the feature is
 * off (the API answers 404 `aggregates_off`).
 */
export async function loadAggregateCard(share: AggregateShare): Promise<ShareCardData | null> {
  if (!parseBillId(share.billId)) return null;
  const [response, aggregates] = await Promise.all([
    orNotFound(getBill(share.billId)),
    orNotFound(getBillAggregates(share.billId)),
  ]);
  if (!response?.bill || !aggregates) return null;
  const cell = share.scopeKey === "" ? aggregates.national : findCell(aggregates, share.scopeKey);
  if (!isShareableCell(cell)) return null;
  const { bill } = response;
  const input = { billType: bill.bill_type, number: bill.number, congress: bill.congress, title: bill.title };

  return {
    copy: aggregateCardCopy(input, cell),
    actionHref: `/bills/${share.billId}`,
    actionLabel: "How would you vote?",
    about:
      "Someone shared this from Just a Bill, where anyone can read bills in Congress, vote on them, and compare " +
      "their votes with their representatives'. These numbers count opt-in Just a Bill users who chose to vote " +
      "and who say they live in a place, so they can't tell you what a district, a state or the country thinks.",
    methodology: { href: METHODOLOGY_AGGREGATES, label: "How these numbers work" },
  };
}
