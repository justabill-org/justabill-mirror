// Labels for House seats, including the single-seat ones the API returns as district 0
// (docs/design/70-district-lookup.md, "Architecture → Web").

import type { RepsDistrict } from "./types";

/** Places represented in the House by a non-voting delegate. They have no senators. */
export const DELEGATE_AREAS: ReadonlySet<string> = new Set(["DC", "GU", "VI", "AS", "MP"]);

/** Puerto Rico's non-voting Resident Commissioner. It has no senators either. */
export const RESIDENT_COMMISSIONER_AREAS: ReadonlySet<string> = new Set(["PR"]);

const PLACE_NAMES: Record<string, string> = {
  DC: "the District of Columbia",
  PR: "Puerto Rico",
  GU: "Guam",
  VI: "the U.S. Virgin Islands",
  AS: "American Samoa",
  MP: "the Northern Mariana Islands",
};

/** Whether a state (two-letter code) elects U.S. senators: false for DC and the territories. */
export function hasSenators(state: string): boolean {
  return !DELEGATE_AREAS.has(state) && !RESIDENT_COMMISSIONER_AREAS.has(state);
}

/**
 * The seat's label: "District 10", "At-large" for district 0 in a state (AK, DE, ND, SD, VT, WY),
 * "Delegate (non-voting)" for DC, GU, VI, AS and MP, "Resident Commissioner (non-voting)" for PR.
 */
export function seatLabel(state: string, district: number): string {
  if (RESIDENT_COMMISSIONER_AREAS.has(state)) return "Resident Commissioner (non-voting)";
  if (DELEGATE_AREAS.has(state)) return "Delegate (non-voting)";
  if (district === 0) return "At-large";
  return `District ${district}`;
}

/** "TX-10" for a numbered district, "WY at-large" or "GU delegate (non-voting)" otherwise. */
export function districtName(state: string, district: number): string {
  if (district !== 0 && hasSenators(state)) return `${state}-${district}`;
  return `${state} ${seatLabel(state, district).toLowerCase()}`;
}

/**
 * The label for the representatives of a lookup: the seat's label when the address is in one
 * district, the district names when it straddles several, and "" when it matched none.
 */
export function seatsLabel(districts: readonly RepsDistrict[]): string {
  if (districts.length === 1) return seatLabel(districts[0].state, districts[0].district);
  return districts.map((d) => districtName(d.state, d.district)).join(", ");
}

/**
 * "No U.S. senators represent Guam." when the lookup's districts are all in DC or a territory,
 * or null when some of them are in a state (whose senators the page lists).
 */
export function noSenatorsNote(districts: readonly RepsDistrict[]): string | null {
  if (districts.length === 0 || districts.some((d) => hasSenators(d.state))) return null;
  const places = [...new Set(districts.map((d) => PLACE_NAMES[d.state] ?? d.state))];
  return `No U.S. senators represent ${places.join(" or ")}.`;
}
