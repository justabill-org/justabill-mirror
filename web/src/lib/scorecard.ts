// The signed-out scorecard: the visitor's votes (kept in this browser) against each member's
// public positions (GET /members/{id}/positions, #213), scored here so the votes never leave the
// device (design docs/design/72-account-free-voting.md, "Scoring"). The server's rule decides
// which roll call is a member's position on a bill; this file only counts.

import { getMember, getMemberPositions, getReps, getRepsAt } from "@/lib/api";
import type { LocalRep, LocalReps } from "@/lib/local/reps";
import type { LocalVotes } from "@/lib/local/votes";
import { isMemberPhotoUrl } from "@/lib/member-photo";
import type {
  MemberDetail,
  MemberPosition,
  MemberPositionsResponse,
  RepsResponse,
  VoteComparison,
} from "@/lib/types";

/** One member's alignment with the visitor. */
export interface MemberScore {
  member_id: string;
  /** Bills where the visitor voted yea or nay and the member did too. */
  compared: number;
  matching: number;
  /** Bills the visitor voted on where the member was present, didn't vote or cast another value. */
  member_absent: number;
  /** round(100 × matching / compared), or null when nothing is compared. */
  alignment_pct: number | null;
  /** Every bill both voted on, newest roll call first. */
  rows: VoteComparison[];
}

/** Member ID → that member's positions, across every congress fetched. */
export type PositionsByMember = Readonly<Record<string, readonly MemberPosition[]>>;

const COUNTED = new Set(["yea", "nay"]);
const BILL_CONGRESS = /^[a-z]+-(\d{1,3})-\d{1,6}$/;

function scoreMember(memberId: string, votes: LocalVotes, positions: readonly MemberPosition[]): MemberScore {
  const rows: VoteComparison[] = [];
  let matching = 0;
  let compared = 0;
  for (const p of positions) {
    const mine = votes[p.bill_id];
    // Skips are stored so /vote doesn't offer the bill again, but they're never scored.
    if (!mine || !COUNTED.has(mine.vote)) continue;
    const memberVote = p.vote.toLowerCase();
    const counted = COUNTED.has(memberVote);
    const matches = counted && memberVote === mine.vote;
    if (counted) compared++;
    if (matches) matching++;
    const congress = billCongress(p.bill_id);
    rows.push({
      bill_id: p.bill_id,
      bill_title: mine.title ?? p.bill_id,
      vote_id: p.vote_id,
      ...(p.chamber ? { chamber: p.chamber } : {}),
      ...(congress === undefined ? {} : { congress }),
      vote_date: p.vote_date,
      question: p.question ?? null,
      user_vote: mine.vote,
      member_vote: memberVote,
      counted,
      matches,
    });
  }
  rows.sort((a, b) => Date.parse(b.vote_date) - Date.parse(a.vote_date) || a.bill_id.localeCompare(b.bill_id));
  return {
    member_id: memberId,
    compared,
    matching,
    member_absent: rows.length - compared,
    alignment_pct: compared === 0 ? null : Math.round((100 * matching) / compared),
    rows,
  };
}

/** Scores the visitor's votes against each member's positions. */
export function score(votes: LocalVotes, positionsByMember: PositionsByMember): Record<string, MemberScore> {
  const out: Record<string, MemberScore> = {};
  for (const [memberId, positions] of Object.entries(positionsByMember)) {
    out[memberId] = scoreMember(memberId, votes, positions);
  }
  return out;
}

/** The congress in a bill ID (`hr-119-1` is the 119th), or undefined for an ID of another shape. */
export function billCongress(billId: string): number | undefined {
  const m = BILL_CONGRESS.exec(billId);
  return m ? Number(m[1]) : undefined;
}

/**
 * How many of the visitor's votes count (yea or nay) in each congress, from the bill IDs. A
 * congress with none is absent; the scorecard's congress switch offers those it has roll calls for.
 */
export function votesByCongress(votes: LocalVotes): Record<number, number> {
  const counts: Record<number, number> = {};
  for (const [billId, v] of Object.entries(votes)) {
    const congress = billCongress(billId);
    if (congress !== undefined && COUNTED.has(v.vote)) counts[congress] = (counts[congress] ?? 0) + 1;
  }
  return counts;
}

/** The congresses the visitor voted yea or nay in, from the bill IDs. */
export function congressesVotedIn(votes: LocalVotes): number[] {
  const set = new Set<number>();
  for (const [billId, v] of Object.entries(votes)) {
    const congress = billCongress(billId);
    if (congress !== undefined && COUNTED.has(v.vote)) set.add(congress);
  }
  return [...set].sort((a, b) => a - b);
}

export type PositionsFetcher = (memberId: string, congress: number) => Promise<MemberPositionsResponse>;

/**
 * Fetches each member's positions in each congress the visitor voted in: public GETs that carry
 * a member ID and a congress, never a vote. A failed request rejects the whole load.
 */
export async function loadPositions(
  members: readonly Pick<LocalRep, "id">[],
  congresses: readonly number[],
  fetchPositions: PositionsFetcher = getMemberPositions
): Promise<PositionsByMember> {
  const entries = await Promise.all(
    members.map(async (m) => {
      const pages = await Promise.all(congresses.map((c) => fetchPositions(m.id, c)));
      return [m.id, pages.flatMap((p) => p.positions ?? [])] as const;
    })
  );
  return Object.fromEntries(entries);
}

function fullName(m: { first_name: string; last_name: string }): string {
  return `${m.first_name} ${m.last_name}`.trim();
}

function seat(d: { state: string; district: number }): { state: string; district: number } {
  return { state: d.state, district: d.district };
}

/**
 * The parts of a rep lookup worth keeping: state, district and members, plus the next election's
 * districts when they differ from today's (#375). The address the lookup echoes is dropped here,
 * so it's never stored.
 */
export function repsFromLookup(res: RepsResponse, lookedUpAt: Date): LocalReps | null {
  const first = res.districts?.[0];
  if (!first) return null;
  const district = res.districts.length === 1 ? first.district : undefined;
  const members: LocalRep[] = [
    ...res.reps.map((m) => ({
      id: m.bioguide_id,
      name: fullName(m),
      party: "",
      chamber: "House",
      state: first.state,
      ...(district === undefined ? {} : { district }),
    })),
    ...res.senators.map((m) => ({ id: m.bioguide_id, name: fullName(m), party: "", chamber: "Senate", state: first.state })),
  ];
  const reps: LocalReps = {
    state: first.state,
    district: district ?? null,
    looked_up_at: lookedUpAt.toISOString(),
    members,
  };
  const election = res.election;
  if (election?.changed && election.districts.length > 0) {
    reps.election = {
      congress: election.congress,
      election_date: election.election_date,
      districts: election.districts.map(seat),
      current: res.districts.map(seat),
    };
  }
  return reps;
}

/** Looks up the reps for an address (the only request that carries it) and keeps what's safe to store. */
export async function lookUpReps(
  address: string,
  now: Date = new Date(),
  lookup: (address: string) => Promise<RepsResponse> = getReps
): Promise<LocalReps | null> {
  return repsFromLookup(await lookup(address.trim()), now);
}

/** A latitude and longitude in degrees, as the browser's geolocation reports them. */
export interface Point {
  lat: number;
  lon: number;
}

/** Looks up the reps at a point (the only request that carries it) and keeps what's safe to store. */
export async function lookUpRepsAt(
  point: Point,
  now: Date = new Date(),
  lookup: (lat: number, lon: number) => Promise<RepsResponse> = getRepsAt
): Promise<LocalReps | null> {
  return repsFromLookup(await lookup(point.lat, point.lon), now);
}

// The photo rules live in a plain module the client photo component can import (#787).
export { MEMBER_PHOTO_ORIGIN, MEMBER_PHOTO_PATH, isMemberPhotoUrl } from "@/lib/member-photo";

/** What a rep card shows beyond the lookup (#667): the member's party and photo. */
export interface RepProfile {
  /** The party of their latest term in the rep's chamber, or "" when no term says. */
  party: string;
  photoUrl?: string;
}

/**
 * The party and photo from GET /members/{id}. The rep lookup carries neither, so the card asks for
 * the member: the party comes from their latest term in the chamber they hold the seat in.
 */
export function repProfile(detail: MemberDetail, rep: Pick<LocalRep, "chamber">): RepProfile {
  const terms = (detail.terms ?? []).filter((t) => t.chamber === rep.chamber);
  const latest = terms.reduce<MemberDetail["terms"][number] | undefined>(
    (best, t) => (!best || t.congress > best.congress ? t : best),
    undefined,
  );
  const profile: RepProfile = { party: latest?.party ?? "" };
  if (isMemberPhotoUrl(detail.photo_url)) profile.photoUrl = detail.photo_url;
  return profile;
}

export type MemberFetcher = (memberId: string) => Promise<MemberDetail>;

/**
 * Each rep's party and photo, by member ID: public GETs that carry only a member ID. Optional, so
 * a rep whose request fails is just left out (their card shows initials and no party).
 */
export async function loadRepProfiles(
  members: readonly Pick<LocalRep, "id" | "chamber">[],
  fetchMember: MemberFetcher = getMember
): Promise<Record<string, RepProfile>> {
  const results = await Promise.allSettled(members.map(async (m) => fetchMember(m.id)));
  const out: Record<string, RepProfile> = {};
  results.forEach((r, i) => {
    if (r.status === "fulfilled") out[members[i].id] = repProfile(r.value, members[i]);
  });
  return out;
}
