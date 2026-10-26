// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { Congress, MemberAttendanceEntry, MemberDetail, MemberTerm, MemberVoteSummary } from "@/lib/types";
import { axeViolations } from "@/test/axe";

// #787: the member page shows who the member is (photo or initials, party, seat), the bill behind
// each recent vote in the site's Yea/Nay words, and the terms on record, for every member alike.

vi.mock("@/lib/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api")>()),
  getMember: vi.fn(),
  getCollaborators: vi.fn(),
  listCongresses: vi.fn(),
}));

const api = await import("@/lib/api");
const { default: MemberPage, generateMetadata } = await import("@/app/(app)/members/[id]/page");

const PHOTO = "https://www.congress.gov/img/member/a000001_200.jpg";

const congresses: Congress[] = [
  { number: 118, start_date: "2023-01-03", end_date: "2025-01-03", is_current: false, has_votes: true },
  { number: 119, start_date: "2025-01-03", is_current: true, has_votes: true },
];

function term(overrides: Partial<MemberTerm>): MemberTerm {
  return { member_id: "A000001", congress: 119, chamber: "House", state: "CA", district: 12, party: "D", ...overrides };
}

function vote(overrides: Partial<MemberVoteSummary>): MemberVoteSummary {
  return {
    vote_id: "house-119-s1-roll001",
    bill_id: "hr-119-808",
    bill_title: "Lamplight Library Hours Act",
    vote_date: "2025-03-04T00:00:00Z",
    question: "On Passage",
    result: "Passed",
    member_vote: "Yea",
    chamber: "House",
    ...overrides,
  };
}

function attendance(overrides: Partial<MemberAttendanceEntry>): MemberAttendanceEntry {
  return {
    congress: 119,
    chamber: "House",
    eligible: 782,
    missed: 44,
    present: 3,
    missed_pct: 5.6,
    chamber_members: 441,
    chamber_median_pct: 2.6,
    computed_at: "2026-10-17T06:12:03Z",
    ...overrides,
  };
}

function member(overrides: Partial<MemberDetail> = {}): MemberDetail {
  return {
    bioguide_id: "A000001",
    first_name: "Ada",
    last_name: "Alvarez",
    photo_url: PHOTO,
    official_url: "https://alvarez.house.gov/",
    terms: [term({ congress: 118 }), term({})],
    recent_votes: [vote({})],
    attendance: { rule: "attendance-v1", congresses: [] },
    ...overrides,
  };
}

async function renderPage(detail: MemberDetail) {
  vi.mocked(api.getMember).mockResolvedValue(detail);
  const page = await MemberPage({ params: Promise.resolve({ id: detail.bioguide_id }) });
  return render(page);
}

function voteRows(): HTMLElement[] {
  return within(screen.getByRole("region", { name: "Recent votes" })).getAllByRole("listitem");
}

function missedVotes(): HTMLElement {
  return screen.getByRole("region", { name: "Missed votes" });
}

function termRows(): HTMLElement[] {
  return within(screen.getByRole("region", { name: "Terms" })).queryAllByRole("listitem");
}

beforeEach(() => {
  vi.mocked(api.getCollaborators).mockResolvedValue({ congress: 119, collaborators: [] });
  vi.mocked(api.listCongresses).mockResolvedValue(congresses);
});

afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

describe("member page header", () => {
  it("shows the photo through next/image, the party, the seat, the congress and the official site", async () => {
    const { container } = await renderPage(member());
    const img = container.querySelector("header img");
    expect(img?.getAttribute("src")).toContain(`/_next/image?url=${encodeURIComponent(PHOTO)}`);
    const header = container.querySelector("header")!;
    expect(header.textContent).toContain("Democrat");
    expect(header.textContent).toContain("Representative, CA-12");
    expect(header.textContent).toContain("119th Congress");
    expect(within(header).getByRole("link", { name: "Official website" })).toHaveProperty(
      "href",
      "https://alvarez.house.gov/",
    );
  });

  it.each([
    ["no photo", undefined],
    ["a photo from another host", "https://example.com/img/member/a000001_200.jpg"],
  ])("shows initials with %s", async (_, photo_url) => {
    const { container } = await renderPage(member({ photo_url }));
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("header")!.textContent).toMatch(/^AA/);
  });

  it("falls back to initials when the photo fails to load", async () => {
    const { container } = await renderPage(member());
    fireEvent.error(container.querySelector("header img")!);
    expect(container.querySelector("header img")).toBeNull();
    expect(container.querySelector("header")!.textContent).toMatch(/^AA/);
  });

  it("links no official site that isn't http(s)", async () => {
    await renderPage(member({ official_url: "javascript:alert(1)" }));
    expect(screen.queryByRole("link", { name: "Official website" })).toBeNull();
  });

  it("shows the seat still held when the member sat in both chambers in one congress", async () => {
    const { container } = await renderPage(
      member({
        terms: [
          term({ end_date: "2025-06-30T00:00:00Z" }),
          term({ chamber: "Senate", district: undefined, party: "D", start_date: "2025-07-01T00:00:00Z" }),
        ],
      }),
    );
    expect(container.querySelector("header")!.textContent).toContain("Senator, CA");
    expect(termRows().map((r) => r.textContent)).toEqual([
      "119th Congress · 2025–2027Senate · CA·Democrat",
      "119th Congress · 2025House · CA-12·Democrat",
    ]);
  });

  it("with no terms on record, shows the name and initials, no seat, and says so", async () => {
    const { container } = await renderPage(member({ terms: [], photo_url: undefined }));
    expect(api.getCollaborators).not.toHaveBeenCalled();
    const header = container.querySelector("header")!;
    expect(header.textContent).toBe("AAAda AlvarezOfficial website");
    expect(screen.getByRole("region", { name: "Terms" }).textContent).toContain("No terms on record.");
  });

  it("is a 404 for a member the API doesn't have", async () => {
    vi.mocked(api.getMember).mockRejectedValue(new api.ApiError(404, "Not Found", ""));
    await expect(MemberPage({ params: Promise.resolve({ id: "Z999999" }) })).rejects.toMatchObject({
      digest: "NEXT_HTTP_ERROR_FALLBACK;404",
    });
  });
});

describe("member page recent votes", () => {
  it("names the bill and links to it, with the question, result, chamber and date", async () => {
    await renderPage(member());
    const [row] = voteRows();
    const link = within(row).getByRole("link");
    expect(link.textContent).toBe("H.R. 808 · Lamplight Library Hours Act");
    expect(link.getAttribute("href")).toBe("/bills/hr-119-808");
    expect(row.textContent).toContain("On Passage · Passed · House · Mar 4, 2025");
  });

  it("shows an unloaded bill's label as text, with no link to a 404", async () => {
    await renderPage(member({ recent_votes: [vote({ bill_id: "s-119-42", bill_title: undefined })] }));
    const [row] = voteRows();
    expect(within(row).queryByRole("link")).toBeNull();
    expect(row.textContent).toContain("S. 42On Passage · Passed");
  });

  it("shows the question of a vote on no bill, or 'Roll call vote' and the chamber", async () => {
    await renderPage(
      member({
        recent_votes: [
          vote({ vote_id: "a", bill_id: undefined, bill_title: undefined, question: "On the Cloture Motion" }),
          vote({ vote_id: "b", bill_id: undefined, bill_title: undefined, question: undefined, result: undefined }),
        ],
      }),
    );
    const [withQuestion, bare] = voteRows();
    expect(withQuestion.textContent).toContain("On the Cloture MotionPassed · House");
    expect(bare.textContent).toContain("Roll call voteHouse · Mar 4, 2025");
  });

  it.each([
    ["Aye", "Yea"],
    ["Yea", "Yea"],
    ["No", "Nay"],
    ["Nay", "Nay"],
    ["Present", "Present"],
    ["Not Voting", "Not voting"],
    ["Guilty", "Guilty"],
    ["Present, giving live pair", "Present, giving live pair"],
  ])("shows the clerk's %j as %j", async (recorded, shown) => {
    await renderPage(member({ recent_votes: [vote({ member_vote: recorded })] }));
    expect(voteRows()[0].textContent).toMatch(new RegExp(`Vote: ${shown}$`));
  });

  it("says when there are no recorded votes", async () => {
    await renderPage(member({ recent_votes: [] }));
    expect(screen.getByRole("region", { name: "Recent votes" }).textContent).toContain("No recorded votes yet.");
  });

  it("says the recent votes couldn't be loaded when their read failed, not that there are none (#929)", async () => {
    await renderPage(member({ recent_votes: null }));
    const section = screen.getByRole("region", { name: "Recent votes" });
    expect(section.textContent).toContain("Recent votes couldn't be loaded right now.");
    expect(section.textContent).not.toContain("No recorded votes yet.");
    expect(within(section).queryAllByRole("listitem")).toHaveLength(0);
  });
});

// #1001: the missed-vote figures under attendance-v1, as counts first and then percentages, next to
// the chamber's median as a plain number.
describe("member page missed votes", () => {
  const withRows = (...congresses: MemberAttendanceEntry[]) =>
    member({ attendance: { rule: "attendance-v1", congresses } });

  it("shows the latest congress as a count of roll calls, its percentage and the chamber median, as text in a list", async () => {
    await renderPage(
      withRows(
        attendance({}),
        attendance({ congress: 118, eligible: 1241, missed: 53, missed_pct: 4.3, chamber_median_pct: 2.3 }),
      ),
    );
    const section = missedVotes();
    const list = section.querySelector("dl");
    expect(list).not.toBeNull();
    const terms = [...list!.querySelectorAll("dt")].map((dt) => dt.textContent);
    const values = [...list!.querySelectorAll("dd")].map((dd) => dd.textContent);
    expect(terms).toEqual(["119th Congress · House", "118th Congress · House"]);
    expect(values).toEqual([
      "44 of 782 roll calls (5.6%)",
      "House median 2.6%",
      "53 of 1,241 roll calls (4.3%) · House median 2.3%",
    ]);
  });

  it("says what's counted and links the Methodology section", async () => {
    await renderPage(withRows(attendance({})));
    const section = missedVotes();
    expect(section.textContent).toContain(
      "Roll-call votes the member was listed on, except quorum calls. “Present” counts as voting. Many things " +
        "keep a member off the floor; this doesn't say why.",
    );
    expect(within(section).getByRole("link", { name: /how we count/i }).getAttribute("href")).toBe(
      "/methodology#missed-votes",
    );
  });

  it("names each chamber of a congress in which the member served in both", async () => {
    await renderPage(
      withRows(
        attendance({ chamber: "House", eligible: 300, missed: 3, missed_pct: 1 }),
        attendance({ chamber: "Senate", eligible: 120, missed: 0, missed_pct: 0, chamber_median_pct: 1.4 }),
      ),
    );
    const values = [...missedVotes().querySelectorAll("dt, dd")].map((el) => el.textContent);
    expect(values).toEqual([
      "119th Congress · House",
      "3 of 300 roll calls (1.0%)",
      "House median 2.6%",
      "119th Congress · Senate",
      "0 of 120 roll calls (0.0%) · Senate median 1.4%",
    ]);
  });

  it("shows the count before the percentage for a member with a handful of roll calls", async () => {
    await renderPage(withRows(attendance({ eligible: 5, missed: 1, present: 0, missed_pct: 20 })));
    expect(missedVotes().querySelector("dd")!.textContent).toBe("1 of 5 roll calls (20.0%)");
  });

  it("says there are none on record, with no 0%, for a member listed on no roll calls", async () => {
    await renderPage(member({ attendance: { rule: "attendance-v1", congresses: [] } }));
    const section = missedVotes();
    expect(section.textContent).toContain("No roll-call votes on record.");
    expect(section.textContent).not.toContain("%");
    expect(section.querySelector("dl")).toBeNull();
  });

  it("says the figures couldn't be loaded when their read failed, not that there are none", async () => {
    await renderPage(member({ attendance: null }));
    const section = missedVotes();
    expect(section.textContent).toContain("Missed votes couldn't be loaded right now.");
    expect(section.textContent).not.toContain("No roll-call votes on record.");
    expect(section.querySelector("dl")).toBeNull();
  });

  it("sits in the right column above Terms", async () => {
    await renderPage(withRows(attendance({})));
    const terms = screen.getByRole("region", { name: "Terms" });
    expect(missedVotes().parentElement).toBe(terms.parentElement);
    expect(missedVotes().compareDocumentPosition(terms) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});

describe("member page terms", () => {
  it("lists each term newest first, names the congresses covered and links the Biographical Directory", async () => {
    await renderPage(
      member({ terms: [term({ congress: 118, district: 0, state: "WY", party: "R" }), term({ party: "D" })] }),
    );
    expect(termRows().map((r) => r.textContent)).toEqual([
      "119th Congress · 2025–2027House · CA-12·Democrat",
      "118th Congress · 2023–2025House · WY at-large·Republican",
    ]);
    const terms = screen.getByRole("region", { name: "Terms" });
    expect(terms.textContent).toContain("Terms in the 118th and 119th Congresses, the ones Just a Bill covers.");
    expect(
      within(terms).getByRole("link", { name: "Biographical Directory of the United States Congress" }),
    ).toHaveProperty("href", "https://bioguide.congress.gov/search/bio/A000001");
  });

  it("still renders when the congress list fails", async () => {
    vi.mocked(api.listCongresses).mockRejectedValue(new Error("down"));
    await renderPage(member());
    expect(screen.getByRole("region", { name: "Terms" }).textContent).toContain(
      "Terms in the congresses Just a Bill covers.",
    );
  });
});

describe("member page", () => {
  it("links to the scorecard", async () => {
    await renderPage(member());
    expect(
      screen.getByRole("link", { name: "See how your votes compare with your representatives" }).getAttribute("href"),
    ).toBe("/scorecard");
  });

  it("has no axe violations with a photo, votes, missed votes, terms and collaborators", async () => {
    vi.mocked(api.getCollaborators).mockResolvedValue({
      congress: 119,
      collaborators: [
        {
          bioguide_id: "B000002",
          first_name: "Ben",
          last_name: "Brooks",
          party: "R",
          state: "TX",
          chamber: "House",
          shared_bills: 3,
        },
      ],
    });
    const { container } = await renderPage(
      member({
        recent_votes: [vote({}), vote({ vote_id: "x", bill_id: "s-119-1", bill_title: undefined })],
        attendance: {
          rule: "attendance-v1",
          congresses: [attendance({}), attendance({ congress: 118, missed_pct: 4.3 })],
        },
      }),
    );
    expect(await axeViolations(container)).toEqual([]);
  });
});

// #1011: the tab, search result and link preview name the member, and the metadata step leaves an
// unknown member's 404 to the page.
describe("member page metadata", () => {
  const params = (id: string) => ({ params: Promise.resolve({ id }) });

  it("names the member and their seat, with the page as its canonical URL", async () => {
    vi.mocked(api.getMember).mockResolvedValue(member());
    const md = await generateMetadata(params("A000001"));
    expect(api.getMember).toHaveBeenCalledWith("A000001");
    expect(md.title).toBe("Rep. Ada Alvarez (D-CA-12) | Just a Bill");
    expect(md.alternates?.canonical).toBe("/members/A000001");
    expect(md.openGraph).toMatchObject({ title: "Rep. Ada Alvarez (D-CA-12)", url: "/members/A000001" });
  });

  it.each([
    ["the API doesn't have", new api.ApiError(404, "Not Found", "")],
    ["the API couldn't read", new api.ApiError(503, "Service Unavailable", "")],
  ])("gives a member %s a plain title kept out of search, without throwing", async (_, err) => {
    vi.mocked(api.getMember).mockRejectedValue(err);
    await expect(generateMetadata(params("Z999999"))).resolves.toEqual({
      title: "Member | Just a Bill",
      robots: { index: false, follow: true },
    });
  });
});
