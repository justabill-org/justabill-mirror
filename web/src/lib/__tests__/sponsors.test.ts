import { describe, it, expect } from "vitest";
import { billSponsors, sponsorsFromJSON, sponsorsFromLinks } from "../sponsors";
import type { Bill, BillSponsorship, Cosponsor, Sponsor } from "../types";

const links: BillSponsorship[] = [
  {
    bioguide_id: "A000001",
    first_name: "Ada",
    last_name: "Alvarez",
    role: "sponsor",
    party: "D",
    state: "CA",
    district: 12,
    sponsored_date: "2025-01-03T00:00:00Z",
    is_original: false,
  },
  {
    bioguide_id: "B000002",
    first_name: "Ben",
    last_name: "Brooks",
    role: "cosponsor",
    party: "R",
    state: "TX",
    district: 7,
    sponsored_date: "2025-01-03T00:00:00Z",
    is_original: true,
  },
  { bioguide_id: "C000003", first_name: "Cora", last_name: "Chen", role: "cosponsor", is_original: false },
];

const jsonSponsors: Sponsor[] = [
  { bioguideId: "A000001", fullName: "Rep. Ada Alvarez [D-CA-12]", party: "D", state: "CA" },
];
const jsonCosponsors: Cosponsor[] = [
  {
    bioguideId: "B000002",
    fullName: "Rep. Ben Brooks [R-TX-7]",
    party: "R",
    state: "TX",
    sponsorshipDate: "2025-01-03",
    isOriginalCosponsor: true,
  },
];

describe("sponsorsFromLinks", () => {
  it("splits the sponsor from cosponsors and keeps the API order", () => {
    const got = sponsorsFromLinks(links);
    expect(got?.sponsor).toEqual({
      bioguideId: "A000001",
      name: "Ada Alvarez",
      party: "D",
      state: "CA",
      district: 12,
      sponsoredDate: "2025-01-03T00:00:00Z",
      isOriginal: false,
    });
    expect(got?.cosponsors.map((c) => c.bioguideId)).toEqual(["B000002", "C000003"]);
    expect(got?.cosponsors[0].isOriginal).toBe(true);
    // No term in the bill's congress: party and state stay unknown.
    expect(got?.cosponsors[1].party).toBeUndefined();
  });

  it("returns null without rows, so the page falls back to the JSON", () => {
    expect(sponsorsFromLinks(undefined)).toBeNull();
    expect(sponsorsFromLinks(null)).toBeNull();
    expect(sponsorsFromLinks([])).toBeNull();
  });

  it("returns null when a member isn't synced (no name to show)", () => {
    const unsynced: BillSponsorship = {
      bioguide_id: "Z999999",
      first_name: "",
      last_name: "",
      role: "cosponsor",
      is_original: false,
    };
    expect(sponsorsFromLinks([...links, unsynced])).toBeNull();
  });

  it("handles cosponsors without a sponsor row", () => {
    const got = sponsorsFromLinks(links.slice(1));
    expect(got?.sponsor).toBeUndefined();
    expect(got?.cosponsors).toHaveLength(2);
  });
});

describe("sponsorsFromJSON", () => {
  it("strips the bracketed party and district from Congress.gov names", () => {
    const got = sponsorsFromJSON(jsonSponsors, jsonCosponsors);
    expect(got.sponsor?.name).toBe("Rep. Ada Alvarez");
    expect(got.sponsor?.isOriginal).toBe(false);
    expect(got.cosponsors).toEqual([
      {
        bioguideId: "B000002",
        name: "Rep. Ben Brooks",
        party: "R",
        state: "TX",
        sponsoredDate: "2025-01-03",
        isOriginal: true,
      },
    ]);
  });

  it("is empty without JSON", () => {
    expect(sponsorsFromJSON(undefined, undefined)).toEqual({ sponsor: undefined, cosponsors: [] });
  });
});

describe("billSponsors", () => {
  const bill = { sponsors: jsonSponsors, cosponsors: jsonCosponsors } as Bill;

  it("prefers the link table", () => {
    expect(billSponsors({ bill, sponsorships: links }).sponsor?.name).toBe("Ada Alvarez");
  });

  it("falls back to the JSON when the link table has nothing", () => {
    expect(billSponsors({ bill, sponsorships: [] }).sponsor?.name).toBe("Rep. Ada Alvarez");
    expect(billSponsors({ bill }).cosponsors).toHaveLength(1);
  });
});
