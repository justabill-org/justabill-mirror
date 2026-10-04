// Sponsors for the bill page. They come from the bill_sponsorships link table when it has rows
// for the bill. The Congress.gov JSON on the bill row is kept as a read cache and used until
// backfill-links has written the link rows, or when the link read fails.

import type { BillDetailResponse, BillSponsorship, Cosponsor, Sponsor } from "./types";

export interface SponsorEntry {
  bioguideId: string;
  name: string;
  party?: string;
  state?: string;
  district?: number;
  sponsoredDate?: string;
  isOriginal: boolean;
}

export interface BillSponsors {
  sponsor?: SponsorEntry;
  cosponsors: SponsorEntry[];
}

/**
 * Converts link-table rows (already ordered by the API: sponsor, then cosponsors by date).
 * Returns null when there are none or a member isn't synced yet (no name to show), so the
 * caller falls back to the JSON.
 */
export function sponsorsFromLinks(rows: BillSponsorship[] | null | undefined): BillSponsors | null {
  if (!rows?.length || rows.some((r) => !r.first_name && !r.last_name)) return null;
  const toEntry = (r: BillSponsorship): SponsorEntry => ({
    bioguideId: r.bioguide_id,
    name: `${r.first_name} ${r.last_name}`.trim(),
    party: r.party,
    state: r.state,
    district: r.district,
    sponsoredDate: r.sponsored_date,
    isOriginal: r.is_original,
  });
  const sponsor = rows.find((r) => r.role === "sponsor");
  return {
    sponsor: sponsor && toEntry(sponsor),
    cosponsors: rows.filter((r) => r.role === "cosponsor").map(toEntry),
  };
}

/** Converts the Congress.gov JSON. "Rep. Ada Alvarez [D-CA-12]" becomes "Rep. Ada Alvarez". */
export function sponsorsFromJSON(sponsors: Sponsor[] | undefined, cosponsors: Cosponsor[] | undefined): BillSponsors {
  const toEntry = (s: Sponsor | Cosponsor): SponsorEntry => ({
    bioguideId: s.bioguideId,
    name: s.fullName.split("[")[0].trim(),
    party: s.party,
    state: s.state,
    sponsoredDate: "sponsorshipDate" in s ? s.sponsorshipDate : undefined,
    isOriginal: "isOriginalCosponsor" in s ? s.isOriginalCosponsor : false,
  });
  const first = sponsors?.[0];
  return {
    sponsor: first && toEntry(first),
    cosponsors: (cosponsors ?? []).map(toEntry),
  };
}

/** The bill's sponsors from the link table, or from the JSON when the link table has none. */
export function billSponsors({ bill, sponsorships }: Pick<BillDetailResponse, "bill" | "sponsorships">): BillSponsors {
  return sponsorsFromLinks(sponsorships) ?? sponsorsFromJSON(bill.sponsors, bill.cosponsors);
}
