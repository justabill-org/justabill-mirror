import { describe, it, expect } from "vitest";
import { memberLabel, memberMetadata, unavailableMemberMetadata } from "../member-metadata";
import type { MemberDetail, MemberTerm } from "../types";

// #1011: a member's page is titled for the member, not the site's home page, in tabs, search
// results and link previews.

function term(overrides: Partial<MemberTerm>): MemberTerm {
  return { member_id: "A000001", congress: 119, chamber: "House", state: "CA", district: 12, party: "D", ...overrides };
}

function member(terms: MemberTerm[], overrides: Partial<MemberDetail> = {}): MemberDetail {
  return {
    bioguide_id: "A000001",
    first_name: "Ada",
    last_name: "Alvarez",
    photo_url: "https://www.congress.gov/img/member/a000001_200.jpg",
    official_url: "https://alvarez.house.gov/",
    terms,
    recent_votes: [],
    attendance: { rule: "attendance-v1", congresses: [] },
    ...overrides,
  };
}

describe("memberLabel", () => {
  it.each([
    ["a representative of a numbered district", [term({})], "Rep. Ada Alvarez (D-CA-12)"],
    ["a senator, by state", [term({ chamber: "Senate", state: "TX", district: undefined, party: "R" })], "Sen. Ada Alvarez (R-TX)"],
    ["an at-large representative, by state", [term({ state: "WY", district: 0, party: "R" })], "Rep. Ada Alvarez (R-WY)"],
    ["a delegate", [term({ state: "DC", district: 0 })], "Del. Ada Alvarez (D-DC)"],
    ["Puerto Rico's Resident Commissioner", [term({ state: "PR", district: 0, party: "R" })], "Res. Comm. Ada Alvarez (R-PR)"],
    ["a member with no party on record", [term({ party: "" })], "Rep. Ada Alvarez (CA-12)"],
    ["a member with no terms on record, by name only", [], "Ada Alvarez"],
  ])("names %s", (_, terms, want) => {
    expect(memberLabel(member(terms))).toBe(want);
  });

  it("uses the latest term: a representative who became a senator is titled as a senator", () => {
    const terms = [term({ congress: 117 }), term({ congress: 119, chamber: "Senate", district: undefined }), term({ congress: 118 })];
    expect(memberLabel(member(terms))).toBe("Sen. Ada Alvarez (D-CA)");
  });

  it("uses the seat still held when the member sat in both chambers in one congress", () => {
    const terms = [
      term({ congress: 119, end_date: "2025-06-01" }),
      term({ congress: 119, chamber: "Senate", district: undefined, start_date: "2025-06-02" }),
    ];
    expect(memberLabel(member(terms))).toBe("Sen. Ada Alvarez (D-CA)");
  });
});

describe("memberMetadata", () => {
  it("titles the page with the member, describes what it shows and links to itself", () => {
    const md = memberMetadata(member([term({})]));
    expect(md.title).toBe("Rep. Ada Alvarez (D-CA-12) | Just a Bill");
    expect(md.description).toBe(
      "How Rep. Ada Alvarez (D-CA-12) voted on recent roll calls, their terms in Congress and the members they work with most."
    );
    expect(md.alternates?.canonical).toBe("/members/A000001");
    expect(md.openGraph).toMatchObject({
      title: "Rep. Ada Alvarez (D-CA-12)",
      description: md.description,
      url: "/members/A000001",
      type: "profile",
      firstName: "Ada",
      lastName: "Alvarez",
    });
    expect(md.robots).toBeUndefined();
  });

  // A page that sets openGraph loses the root's file-based og:image (Next merges metadata
  // shallowly), so the member page names the site's card itself; a member card is #88's question.
  // Leaving twitter unset keeps the root twitter-image and lets Next copy the title and description.
  it("keeps the site's card as the preview image", () => {
    const md = memberMetadata(member([term({})]));
    expect(md.openGraph?.images).toEqual([
      {
        url: "/opengraph-image",
        width: 1200,
        height: 630,
        alt: "Just a Bill: read bills in Congress, vote on them, and see how your representatives voted",
        type: "image/png",
      },
    ]);
    expect(md).not.toHaveProperty("twitter");
  });

  it("keeps long names to search-result lengths", () => {
    const md = memberMetadata(member([term({})], { first_name: "Name ".repeat(30), last_name: "Last" }));
    expect(String(md.title).length).toBeLessThanOrEqual(90 + " | Just a Bill".length);
    expect(String(md.description).length).toBeLessThanOrEqual(160);
  });

  it("encodes the ID in the canonical URL", () => {
    const md = memberMetadata(member([term({})], { bioguide_id: "A 1/2" }));
    expect(md.alternates?.canonical).toBe("/members/A%201%2F2");
  });

  it("keeps an unreadable member out of search results", () => {
    expect(unavailableMemberMetadata.title).toBe("Member | Just a Bill");
    expect(unavailableMemberMetadata.robots).toEqual({ index: false, follow: true });
  });
});
