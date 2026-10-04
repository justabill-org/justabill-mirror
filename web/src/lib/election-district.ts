// The "you vote in" line in find-my-reps (#375, docs/design/237-election-district.md): when an
// address votes in a different House district at the next general election than the one it's in
// today (a mid-decade redraw), say which, with the dates and years taken from the API's
// election block, so a 121st-congress row in the API's map table needs no copy change.

import { districtName } from "./districts";
import { ordinal } from "./graph";
import type { RepsDistrict } from "./types";

type Seat = Pick<RepsDistrict, "state" | "district">;

/** The `election` block of a /reps response, or the copy of it the browser keeps. */
export interface ElectionBlock {
  congress: number;
  /** YYYY-MM-DD. */
  election_date: string;
  districts: readonly Seat[];
  changed: boolean;
}

/** What `electionDistrictLine` reads: today's districts and, when the API knows it, the election block. */
export interface ElectionLookup {
  districts: readonly Seat[];
  election?: ElectionBlock | null;
}

/** The line, split so the district names can be bold, and the map's source. */
export interface ElectionDistrictLine {
  lead: string;
  /** "TX-10", or "TX-10 or TX-21" when the address matches several. */
  districts: string;
  rest: string;
  /** Where the map comes from; the page follows it with the vote.gov link. */
  source: string;
}

const ISO_DATE = /^(\d{4})-(\d{2})-(\d{2})$/;

/** "November 3, 2026" for a calendar date, whatever the visitor's time zone. */
function longDate(year: number, month: number, day: number): string {
  return new Date(Date.UTC(year, month - 1, day)).toLocaleDateString("en-US", {
    timeZone: "UTC",
    month: "long",
    day: "numeric",
    year: "numeric",
  });
}

/** Today in the visitor's time zone as YYYY-MM-DD, so Election Day ends at their midnight. */
function localDay(today: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${today.getFullYear()}-${pad(today.getMonth() + 1)}-${pad(today.getDate())}`;
}

function names(seats: readonly Seat[]): string {
  return [...new Set(seats.map((s) => districtName(s.state, s.district)))].join(" or ");
}

/**
 * The line to show under the House member, or null when there's nothing to say: no election
 * block, districts that didn't change, or a date past January 3 after the election, when the
 * new congress is seated and the lookup's current districts are already the new ones.
 */
export function electionDistrictLine(lookup: ElectionLookup, today: Date): ElectionDistrictLine | null {
  const election = lookup.election;
  const match = election ? ISO_DATE.exec(election.election_date) : null;
  if (!election?.changed || !match || election.districts.length === 0) return null;
  const year = Number(match[1]);
  const seated = `${year + 1}-01-03`;
  const day = localDay(today);
  if (day >= seated) return null;

  const next = names(election.districts);
  const source =
    `Map: U.S. Census Bureau, ${ordinal(election.congress)} Congress districts as submitted by the state.`;
  if (day > election.election_date) {
    return { lead: `From ${longDate(year + 1, 1, 3)}, this address is in `, districts: next, rest: ".", source };
  }
  const current = names(lookup.districts);
  const changed = `District lines changed for ${year}`;
  const rest = current
    ? `. ${changed}, so this isn't the district your current representative holds (${current}).`
    : `. ${changed}.`;
  return {
    lead: `On ${longDate(year, Number(match[2]), Number(match[3]))}, this address votes in the election for `,
    districts: next,
    rest,
    source,
  };
}
