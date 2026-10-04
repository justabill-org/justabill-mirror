// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { axeViolations } from "@/test/axe";
import { SwipeCard } from "../vote/swipe-card";
import type { Bill, BillCardFacts, BillDetailResponse, BillLawChangesResponse, BillSummary } from "@/lib/types";

// /vote's card and its Details sheet (#717): the sheet answers what the card can't, loads the
// bill's record when it opens, and takes the vote itself.

const api = vi.hoisted(() => ({ getBill: vi.fn(), getBillLawChanges: vi.fn() }));
vi.mock("@/lib/api", async (importOriginal) => ({ ...(await importOriginal<object>()), ...api }));
vi.mock("@/lib/obs/browser", () => ({ reportError: vi.fn() }));

afterEach(() => {
  cleanup();
  api.getBill.mockReset();
  api.getBillLawChanges.mockReset();
});

const bill = {
  id: "hr-119-1",
  congress: 119,
  bill_type: "hr",
  number: 1,
  title: "Companion Act",
  current_status: "became_law",
  status_date: "2025-12-26T00:00:00Z",
  introduced_date: "2025-01-03T00:00:00Z",
  sponsors: [{ bioguideId: "M000001", fullName: "Rep. Moore, Blake D. [R-UT-1]", party: "R", state: "UT" }],
} as Bill;

// The card's facts as `include=card` serves them (#705): the API picks each chamber's final vote.
const card: BillCardFacts = {
  crs: null,
  passage: [
    {
      chamber: "house",
      method: "roll",
      date: "2025-01-21T00:00:00Z",
      question: "On Motion to Suspend the Rules and Pass, as Amended",
      result: "Passed",
      roll_number: 19,
      yeas: 413,
      nays: 0,
      not_voting: 19,
    },
    { chamber: "senate", method: "voice", date: "2025-12-16T00:00:00Z", result: "Passed" },
  ],
  enacted: { date: "2025-12-26T00:00:00Z", law_type: "public", law_number: "119-62" },
  law_change_count: 5,
};

function detail(extra: Partial<BillDetailResponse> = {}): BillDetailResponse {
  return {
    bill,
    actions: [{ action_date: "2025-12-26", action_text: "Became Public Law No: 119-62." }],
    summary: null,
    crs_summary: null,
    sponsorships: [
      { bioguide_id: "M000001", first_name: "Blake", last_name: "Moore", role: "sponsor", party: "R", state: "UT", is_original: true },
      { bioguide_id: "A1", first_name: "Ada", last_name: "Alvarez", role: "cosponsor", party: "D", state: "CA", is_original: true },
      { bioguide_id: "B1", first_name: "Ben", last_name: "Brown", role: "cosponsor", party: "R", state: "TX", is_original: false },
      { bioguide_id: "C1", first_name: "Cy", last_name: "Clark", role: "cosponsor", party: "R", state: "OH", is_original: false },
    ],
    ...extra,
  } as unknown as BillDetailResponse;
}

function change(n: number) {
  return {
    section_id: `usc-16-${n}`,
    in_us_code: true,
    loaded: true,
    title_number: 16,
    section_number: String(n),
    heading: `Section ${n}`,
    change_kind: "amended",
    cite_text: null,
    subsection_path: null,
    instruction: null,
    explanation: `Explains ${n}.`,
    also_changed_by: [],
  };
}

function open(summary: BillSummary | null, onVote: (v: string) => void = () => {}, facts: BillCardFacts | null = null) {
  render(<SwipeCard bill={bill} summary={summary} card={facts} onVote={onVote} />);
  fireEvent.click(screen.getByRole("button", { name: "Details" }));
  return screen.getByRole("dialog", { name: "Companion Act" });
}

describe("SwipeCard details", () => {
  it('labels the AI summary, shows "Who it affects" and the provenance line at once', () => {
    api.getBill.mockReturnValue(new Promise(() => {}));
    api.getBillLawChanges.mockReturnValue(new Promise(() => {}));
    const dialog = open({
      bill_id: bill.id,
      short_summary: "Pairs votes.",
      who_it_affects: "Voters.",
      model_used: "gemini-3.5-flash",
      source_version_code: "rh",
      source_version_name: "Reported in House",
    });
    expect(within(dialog).getByRole("heading", { name: "AI summary" })).toBeTruthy();
    expect(dialog.textContent).toContain("Written by AI");
    expect(within(dialog).getByRole("heading", { name: "Who it affects" })).toBeTruthy();
    expect(dialog.textContent).toContain("Voters.");
    expect(dialog.textContent).not.toMatch(/why it matters/i);
    expect(dialog.textContent).toContain("Based on the Reported in House text · gemini-3.5-flash");
    expect(within(dialog).getByRole("status", { name: "Loading more about this bill" })).toBeTruthy();
  });

  it("leaves the section out without who_it_affects", () => {
    api.getBill.mockReturnValue(new Promise(() => {}));
    api.getBillLawChanges.mockReturnValue(new Promise(() => {}));
    const none = open({ bill_id: bill.id, short_summary: "Pairs votes." });
    expect(none.textContent).not.toContain("Who it affects");
  });

  it("shows only the model for an older summary", () => {
    api.getBill.mockReturnValue(new Promise(() => {}));
    api.getBillLawChanges.mockReturnValue(new Promise(() => {}));
    const dialog = open({ bill_id: bill.id, short_summary: "Pairs votes.", model_used: "gemini-2.5-flash" });
    expect(dialog.textContent).toContain("Model: gemini-2.5-flash");
    expect(dialog.textContent).not.toContain("Based on");
  });

  it("loads the record when it opens: law changes, each chamber's final vote, the backers and where it stands", async () => {
    api.getBill.mockResolvedValue(detail());
    const changes = { changes: [1, 2, 3, 4, 5].map(change), ai_generated: true } as unknown as BillLawChangesResponse;
    api.getBillLawChanges.mockResolvedValue(changes);
    const dialog = open(null, () => {}, card);
    expect(api.getBill).toHaveBeenCalledWith("hr-119-1");

    const law = (await within(dialog).findByRole("heading", { name: "What it changes in law" })).parentElement!;
    expect(within(law).getAllByRole("listitem")).toHaveLength(3);
    expect(law.textContent).toContain("written by AI");
    expect(law.textContent).toContain("And 2 more on the bill page.");

    const votes = within(dialog).getByRole("heading", { name: "How Congress voted" }).parentElement!;
    expect(within(votes).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "House: Passed, 413–0 · Jan 21, 2025On Motion to Suspend the Rules and Pass, as Amended · Roll call 19 · 19 not voting",
      "Senate: passed by voice vote · Dec 16, 2025No individual votes were recorded.",
    ]);

    const backers = within(dialog).getByRole("heading", { name: "Who backed it" }).parentElement!;
    expect(backers.textContent).toContain("Sponsored by Blake Moore (R-UT)");
    expect(backers.textContent).toContain("3 cosponsors: 1 Democrat, 2 Republicans.");

    const stands = within(dialog).getByRole("heading", { name: "Where it stands" }).parentElement!;
    expect(stands.textContent).toContain("Became law Dec 26, 2025 · Public Law 119-62");
    expect(stands.textContent).toContain("Introduced Jan 3, 2025");
    expect(within(dialog).getByRole("link", { name: /Open the bill page/ }).getAttribute("href")).toBe("/bills/hr-119-1");
    expect(await axeViolations(dialog)).toEqual([]);
  });

  it("shows the official CRS summary when there's no AI summary, and says when there's neither", async () => {
    const crs = {
      bill_id: bill.id,
      version_code: "00",
      action_date: "2025-01-03",
      action_desc: "Introduced in House",
      text: "Companion Act\n\nThis bill pairs votes.",
      updated_at: "2025-02-01T00:00:00Z",
    };
    api.getBill.mockResolvedValue(detail({ crs_summary: crs }));
    api.getBillLawChanges.mockRejectedValue(new Error("none"));
    const dialog = open(null);
    const official = (await within(dialog).findByRole("heading", { name: "Official summary" })).parentElement!;
    expect(official.textContent).toContain("Congressional Research Service, of the bill as Introduced in House");
    expect(official.textContent).toContain("This bill pairs votes.");
    expect(within(dialog).queryByRole("heading", { name: "AI summary" })).toBeNull();
    expect(within(dialog).queryByRole("heading", { name: "What it changes in law" })).toBeNull();
    cleanup();

    api.getBill.mockResolvedValue(detail());
    const bare = open(null);
    expect(await within(bare).findByText(/This bill has no summary yet\./)).toBeTruthy();
  });

  it("keeps what it has when the record doesn't load, and tries again", async () => {
    api.getBill.mockRejectedValueOnce(new Error("down"));
    api.getBillLawChanges.mockResolvedValue(null);
    const dialog = open({ bill_id: bill.id, short_summary: "Pairs votes." });
    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("couldn't be loaded right now");
    expect(dialog.textContent).toContain("Pairs votes.");

    api.getBill.mockResolvedValue(detail());
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await within(dialog).findByRole("heading", { name: "Who backed it" })).toBeTruthy();
    expect(within(dialog).queryByRole("alert")).toBeNull();
  });

  it("opens as a popup in the middle of the screen, not a sheet from the bottom (#717)", () => {
    api.getBill.mockReturnValue(new Promise(() => {}));
    api.getBillLawChanges.mockReturnValue(new Promise(() => {}));
    const panel = open(null).querySelector(".bg-background") as HTMLElement;
    expect(panel.className).toContain("m-auto");
    expect(panel.className).not.toContain("slide-in-from-bottom");
    expect(panel.className).not.toContain("bottom-0");
  });

  it("takes the vote from the sheet and closes it", () => {
    api.getBill.mockReturnValue(new Promise(() => {}));
    api.getBillLawChanges.mockReturnValue(new Promise(() => {}));
    const onVote = vi.fn();
    const dialog = open(null, onVote);
    expect(within(dialog).getAllByRole("button").map((b) => b.textContent)).toEqual(["Close", "Nay", "Skip", "Yea"]);
    fireEvent.click(within(dialog).getByRole("button", { name: "Yea" }));
    expect(onVote).toHaveBeenCalledWith("yea");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("doesn't show how representatives or other users voted before the reader votes", async () => {
    api.getBill.mockResolvedValue(detail());
    api.getBillLawChanges.mockResolvedValue(null);
    const dialog = open(null);
    await within(dialog).findByRole("heading", { name: "Who backed it" });
    expect(dialog.textContent).not.toMatch(/your representatives|users voted/i);
  });
});

// Share on the /vote card (#811): it shares the bill's own page and never moves the deck.
describe("SwipeCard Share", () => {
  function deal(onVote = vi.fn()) {
    render(<SwipeCard bill={bill} summary={null} card={null} onVote={onVote} />);
    const card = screen.getByRole("heading", { level: 2, name: "Companion Act" }).closest(".cursor-grab") as HTMLElement;
    return { onVote, card, share: screen.getByRole("button", { name: "Share H.R. 1" }) };
  }

  // A drag to the right, past the threshold: a Yea when the card takes it.
  function swipeRight(from: HTMLElement, over: HTMLElement) {
    fireEvent.mouseDown(from, { clientX: 0, clientY: 0 });
    fireEvent.mouseMove(over, { clientX: 200, clientY: 0 });
    fireEvent.mouseUp(over);
  }

  it("votes on a swipe while no sheet is open", () => {
    const { onVote, card } = deal();
    swipeRight(card, card);
    expect(onVote).toHaveBeenCalledWith("yea");
  });

  it("sits beside Details and opens the bill's Share sheet with the bill's own link, not the deck's", () => {
    window.history.replaceState(null, "", "/vote/v/treatment?stage=law&policy_area=Health");
    const { onVote, share } = deal();
    expect(share.parentElement).toBe(screen.getByRole("button", { name: "Details" }).parentElement);
    fireEvent.click(share);
    const sheet = screen.getByRole("dialog", { name: "Share this bill" });
    expect(within(sheet).getByLabelText("Link")).toHaveProperty("value", `${window.location.origin}/bills/hr-119-1`);
    expect(onVote).not.toHaveBeenCalled();
    // Outside the card: inside it, the card's select-none would keep the link from being selected.
    expect(sheet.closest(".select-none")).toBeNull();
  });

  it("never starts a drag from a press on Share, by mouse or touch", () => {
    const { onVote, card, share } = deal();
    swipeRight(share, card);
    fireEvent.touchStart(share, { touches: [{ clientX: 0, clientY: 0 }] });
    fireEvent.touchMove(card, { touches: [{ clientX: 200, clientY: 0 }] });
    fireEvent.touchEnd(card);
    expect(onVote).not.toHaveBeenCalled();
  });

  it("ignores swipes while the sheet is open, and closing it leaves the same card", () => {
    const { onVote, card, share } = deal();
    share.focus();
    fireEvent.click(share);
    swipeRight(card, card);
    expect(onVote).not.toHaveBeenCalled();
    expect(card.style.transform).toBe("");

    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("heading", { level: 2, name: "Companion Act" })).toBeTruthy();
    expect(document.activeElement).toBe(share);
    expect(onVote).not.toHaveBeenCalled();
  });

  it("has no axe violations with the sheet open", async () => {
    const { share } = deal();
    fireEvent.click(share);
    expect(await axeViolations(document.body)).toEqual([]);
  });
});
