// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { axeViolations } from "@/test/axe";
import * as api from "@/lib/api";
import type {
  Bill,
  BillCardFacts,
  BillDetailResponse,
  BillLawChangesResponse,
  BillSummary,
  LawChangeEntry,
} from "@/lib/types";
import { SwipeCard } from "../vote/swipe-card";

// The /vote card of #662: each acceptance criterion against the facts GET /bills?include=card
// serves (#705), and the Details popup (#717) that reads the rest of the record when it opens.

vi.mock("@/lib/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api")>()),
  getBill: vi.fn(),
  getBillLawChanges: vi.fn(),
}));

afterEach(cleanup);
beforeEach(() => {
  vi.mocked(api.getBill).mockReset();
  vi.mocked(api.getBillLawChanges).mockReset();
});

// H.R. 187 of the 119th as the API serves it (src/lib/examples/bill-detail.json).
const bill = {
  id: "hr-119-187",
  congress: 119,
  bill_type: "hr",
  number: 187,
  title: "MAPWaters Act of 2025",
  policy_area: "Public Lands and Natural Resources",
  current_status: "became_law",
  status_date: "2025-12-26T00:00:00Z",
  introduced_date: "2025-01-03T00:00:00Z",
  sponsors: [{ bioguideId: "W000821", fullName: "Rep. Westerman, Bruce [R-AR-4]", party: "R", state: "AR" }],
} as Bill;

const crs = {
  version_code: "49",
  action_date: "2025-12-26T00:00:00Z",
  action_desc: "Public Law",
  lead: "This act requires federal land management agencies to make data about outdoor recreation available.",
};

const lawCard: BillCardFacts = {
  crs,
  passage: [
    {
      chamber: "House",
      method: "roll",
      date: "2025-01-21T00:00:00Z",
      question: "On Motion to Suspend the Rules and Pass, as Amended",
      result: "Passed",
      roll_number: 19,
      yeas: 413,
      nays: 0,
      present: 0,
      not_voting: 19,
    },
    { chamber: "Senate", method: "voice", date: "2025-12-16T00:00:00Z", result: "Passed" },
  ],
  enacted: { date: "2025-12-26T00:00:00Z", law_type: "public", law_number: "119-62" },
  law_change_count: 7,
};

const aiSummary: BillSummary = {
  bill_id: bill.id,
  short_summary: "Puts federal outdoor recreation maps and rules online in one format.",
  long_summary: "The act directs agencies to publish their data.",
  who_it_affects: "Visitors to federal lands.",
  model_used: "gemini-3.8-flash",
};

function change(n: number): LawChangeEntry {
  return {
    section_id: `usc:t16:s${n}`,
    in_us_code: true,
    loaded: true,
    title_number: 16,
    section_number: String(6800 + n),
    heading: `Section heading ${n}`,
    change_kind: "adds",
    cite_text: null,
    subsection_path: null,
    instruction: null,
    explanation: `Explains change ${n}.`,
    also_changed_by: [],
  };
}

const lawChanges: BillLawChangesResponse = {
  bill_id: bill.id,
  changes: [1, 2, 3, 4, 5, 6, 7].map(change),
  ai_generated: true,
  current_release_point: null,
} as BillLawChangesResponse;

const detail = {
  bill,
  crs_summary: {
    bill_id: bill.id,
    version_code: crs.version_code,
    action_date: crs.action_date,
    action_desc: crs.action_desc,
    text: `MAPWaters Act of 2025\n\n${crs.lead}\n\nThe agencies must also publish fishing restrictions.`,
    updated_at: "2026-01-01T00:00:00Z",
  },
} as unknown as BillDetailResponse;

function renderCard(props: { summary?: BillSummary | null; card?: BillCardFacts | null; bill?: Bill } = {}) {
  return render(
    <SwipeCard bill={props.bill ?? bill} summary={props.summary} card={props.card} onVote={() => {}} />
  );
}

/** The card's fact rows as "term: description" lines. */
function factRows(container: HTMLElement): string[] {
  const dl = container.querySelector("dl");
  return [...(dl?.querySelectorAll("dt") ?? [])].map(
    (dt) => `${dt.textContent}: ${dt.nextElementSibling?.textContent?.replace(/\s+/g, " ").trim()}`
  );
}

describe("the /vote card (#662)", () => {
  it("leads with the AI summary, labeled as AI, and says who it affects", () => {
    renderCard({ summary: aiSummary, card: lawCard });
    expect(screen.getByText(aiSummary.short_summary!)).toBeTruthy();
    expect(screen.getByText("AI summary").parentElement?.textContent).toMatch(/not reviewed by a person/);
    expect(screen.getByRole("link", { name: "How it's made" }).getAttribute("href")).toBe("/methodology#summaries");
    expect(screen.getByText("Who it affects:").parentElement?.textContent).toContain("Visitors to federal lands.");
    // The AI summary leads, so the CRS lead waits for Details.
    expect(screen.queryByText(crs.lead)).toBeNull();
  });

  it("falls back to the CRS summary's lead, labeled with its source", () => {
    renderCard({ summary: null, card: lawCard });
    expect(screen.getByText(crs.lead)).toBeTruthy();
    expect(screen.getByText("Official summary").parentElement?.textContent).toBe(
      "Official summary · Congressional Research Service"
    );
    expect(screen.queryByText(/AI summary/)).toBeNull();
    expect(screen.queryByText("Who it affects:")).toBeNull();
  });

  it("says so when there's no summary at all", () => {
    renderCard({ summary: null, card: null });
    expect(screen.getByText(/No summary of this bill yet/)).toBeTruthy();
  });

  it("shows how each chamber passed it, when it became law with its number, and the sponsor", () => {
    const { container } = renderCard({ summary: aiSummary, card: lawCard });
    expect(factRows(container)).toEqual([
      "Passed: House: 413–0 · Senate: voice vote",
      "Became law: Dec 26, 2025 · Public Law 119-62",
      // The party dot's screen-reader name, then the name, and the party and state in text too.
      "Sponsor: RepublicanRep. Westerman, Bruce(R-AR)",
    ]);
  });

  it("words unanimous consent, and says a final vote failed rather than calling it passed", () => {
    const card: BillCardFacts = {
      ...lawCard,
      passage: [
        { chamber: "senate", method: "uc", date: "2025-03-01T00:00:00Z", result: "Passed" },
        { ...lawCard.passage[0], date: "2025-04-01T00:00:00Z", result: "Failed", yeas: 190, nays: 230 },
      ],
      enacted: null,
    };
    const { container } = renderCard({ card, bill: { ...bill, current_status: "passed_senate" } as Bill });
    expect(factRows(container)[0]).toBe("Final votes: Senate: unanimous consent · House: failed 190–230");
  });

  it("shows the status of a bill that isn't law, and the date alone when the action named no law number", () => {
    const pending = { ...bill, current_status: "passed_house", status_date: "2025-01-21T00:00:00Z" } as Bill;
    const card = { ...lawCard, passage: [lawCard.passage[0]], enacted: null };
    const { container } = renderCard({ card, bill: pending });
    expect(factRows(container)).toContain("Status: Passed House · Jan 21, 2025");
    cleanup();
    const { container: law } = renderCard({ card: { ...lawCard, enacted: { date: "2025-12-26T00:00:00Z" } } });
    expect(factRows(law)).toContain("Became law: Dec 26, 2025");
  });

  it("keeps the bill number on one line, in ink with tabular numerals (#764)", () => {
    renderCard({ card: lawCard });
    // A no-break space between "H.R." and the number, in a span that doesn't wrap.
    const number = screen.getByText((_, el) => el?.tagName === "SPAN" && el.textContent === "H.R.\u00a0187");
    expect(number.className).toContain("whitespace-nowrap");
    expect(number.className.split(" ")).toEqual(expect.arrayContaining(["text-foreground", "tabular-nums"]));
    expect(number.className).not.toMatch(/font-mono|text-link/);
  });

  it("has no axe violations", async () => {
    const { container } = renderCard({ summary: aiSummary, card: lawCard });
    expect(await axeViolations(container)).toEqual([]);
  });
});

describe("the card's Details popup (#662, #717)", () => {
  function openDetails() {
    fireEvent.click(screen.getByRole("button", { name: "Details" }));
    return screen.getByRole("dialog");
  }

  it("shows the card's votes, the law and the CRS lead at once, before the record loads", () => {
    vi.mocked(api.getBill).mockReturnValue(new Promise(() => {}));
    vi.mocked(api.getBillLawChanges).mockReturnValue(new Promise(() => {}));
    renderCard({ card: lawCard });
    const dialog = openDetails();

    const official = within(dialog).getByRole("heading", { name: "Official summary" }).parentElement!;
    expect(official.textContent).toContain(crs.lead);
    const votes = within(dialog).getByRole("heading", { name: "How Congress voted" }).parentElement!;
    expect(votes.textContent).toContain("House: Passed, 413–0");
    expect(votes.textContent).toContain("Roll call 19 · 19 not voting");
    expect(votes.textContent).toContain("Senate: passed by voice vote");
    const stands = within(dialog).getByRole("heading", { name: "Where it stands" }).parentElement!;
    expect(stands.textContent).toContain("Became law Dec 26, 2025 · Public Law 119-62");
    expect(within(dialog).getByRole("status", { name: "Loading more about this bill" })).toBeTruthy();
  });

  it("reads the record once, and shows the first three law changes with a count for the rest", async () => {
    vi.mocked(api.getBill).mockResolvedValue(detail);
    vi.mocked(api.getBillLawChanges).mockResolvedValue(lawChanges);
    renderCard({ summary: aiSummary, card: lawCard });
    const dialog = openDetails();

    // The full CRS summary, folded under the AI summary and labeled with its source.
    expect(await within(dialog).findByText(/must also publish fishing restrictions/)).toBeTruthy();
    expect(within(dialog).getByText(/Congressional Research Service, of the bill as/).textContent).toContain(
      "December 26, 2025"
    );
    expect(within(dialog).getByRole("link", { name: "Read this summary on Congress.gov" }).getAttribute("href")).toBe(
      "https://www.congress.gov/bill/119th-congress/house-bill/187/summary/49"
    );
    expect(within(dialog).getByRole("heading", { name: "AI summary" })).toBeTruthy();
    expect(within(dialog).getByText(aiSummary.long_summary!)).toBeTruthy();

    const changes = within(dialog).getByRole("heading", { name: "What it changes in law" }).parentElement!;
    expect(within(changes).getAllByRole("listitem")).toHaveLength(3);
    expect(changes.textContent).toMatch(/written by AI/);
    expect(changes.textContent).toContain("And 4 more on the bill page.");
    expect(within(dialog).getByRole("link", { name: /Open the bill page/ }).getAttribute("href")).toBe(
      `/bills/${bill.id}`
    );

    // Closing and opening again doesn't read again.
    fireEvent.click(within(dialog).getByRole("button", { name: "Close" }));
    openDetails();
    expect(api.getBill).toHaveBeenCalledOnce();
    expect(api.getBillLawChanges).toHaveBeenCalledOnce();
  });

  it("doesn't ask for law changes when the card says there are none", async () => {
    vi.mocked(api.getBill).mockResolvedValue(detail);
    renderCard({ card: { ...lawCard, law_change_count: 0 } });
    const dialog = openDetails();
    expect(await within(dialog).findByText(/must also publish fishing restrictions/)).toBeTruthy();
    expect(api.getBillLawChanges).not.toHaveBeenCalled();
    expect(within(dialog).queryByRole("heading", { name: "What it changes in law" })).toBeNull();
  });

  it("keeps the CRS lead and the card's facts when the read fails, and tries again", async () => {
    vi.mocked(api.getBill).mockRejectedValueOnce(new Error("offline"));
    vi.mocked(api.getBillLawChanges).mockResolvedValue(lawChanges);
    renderCard({ card: lawCard });
    const dialog = openDetails();

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("couldn't be loaded right now");
    expect(within(dialog).getByText(crs.lead)).toBeTruthy();
    expect(within(dialog).getByRole("heading", { name: "How Congress voted" })).toBeTruthy();

    vi.mocked(api.getBill).mockResolvedValue(detail);
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await within(dialog).findByText(/must also publish fishing restrictions/)).toBeTruthy();
    expect(api.getBill).toHaveBeenCalledTimes(2);
  });

  it("has no axe violations with everything loaded", async () => {
    vi.mocked(api.getBill).mockResolvedValue(detail);
    vi.mocked(api.getBillLawChanges).mockResolvedValue(lawChanges);
    renderCard({ summary: aiSummary, card: lawCard });
    const dialog = openDetails();
    await within(dialog).findByText(/must also publish fishing restrictions/);
    expect(await axeViolations(dialog)).toEqual([]);
  });
});
