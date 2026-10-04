// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { RepCard } from "../scorecard/rep-card";
import { ScorecardView, type ScorecardViewProps } from "../scorecard/local-scorecard";
import type { VoteComparison } from "@/lib/types";
import type { LocalRep, LocalReps } from "@/lib/local/reps";
import type { MemberScore } from "@/lib/scorecard";
import { axeViolations } from "@/test/axe";

// #667: a card per representative, a way into /vote before any votes, and "My votes" folded away.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const rep: LocalRep = { id: "C000003", name: "Cora Chen", party: "R", chamber: "Senate", state: "CA" };
const house: LocalRep = { id: "A000001", name: "Ada Alvarez", party: "", chamber: "House", state: "CA", district: 12 };
const PHOTO = "https://www.congress.gov/img/member/c000003_200.jpg";

function row(overrides: Partial<VoteComparison>): VoteComparison {
  return {
    bill_id: "s-119-5",
    bill_title: "Senate Act",
    vote_id: "senate-119-s1-vote00005",
    vote_date: "2025-05-08T17:00:00Z",
    question: "On Passage of the Bill S. 5",
    user_vote: "nay",
    member_vote: "nay",
    counted: true,
    matches: true,
    ...overrides,
  };
}

const rows = [
  row({ bill_id: "hr-119-1", bill_title: "Bill One", user_vote: "yea", member_vote: "yea" }),
  row({ bill_id: "hr-119-2", bill_title: "Bill Two", user_vote: "yea", member_vote: "nay", matches: false }),
  row({ bill_id: "hr-119-3", bill_title: "Bill Three", member_vote: "not_voting", counted: false, matches: false }),
  row({ bill_id: "hr-119-4", bill_title: "Bill Four", congress: 119, chamber: "House" }),
];

const scored: MemberScore = {
  member_id: "C000003",
  matching: 2,
  compared: 3,
  member_absent: 1,
  alignment_pct: 67,
  rows,
};

/** Stubs matchMedia so the sm breakpoint matches (wide) or not (a phone). */
function viewport(wide: boolean) {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) => ({
      matches: wide,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    })),
  );
}

describe("RepCard identity", () => {
  it("shows the photo, the name linking to the member, party, chamber and state", () => {
    render(<RepCard rep={rep} profile={{ party: "R", photoUrl: PHOTO }} />);
    const img = screen.getByRole("presentation", { hidden: true }) as HTMLImageElement;
    expect(img.tagName).toBe("IMG");
    // Served through next/image from our origin, so the CSP's img-src stays 'self'.
    expect(img.getAttribute("src")).toContain(`/_next/image?url=${encodeURIComponent(PHOTO)}`);
    expect(screen.getByRole("link", { name: "Cora Chen" }).getAttribute("href")).toBe("/members/C000003");
    expect(screen.getByText("Republican")).toBeTruthy();
    expect(screen.getByText("Senate · CA")).toBeTruthy();
  });

  it("names a House member's district, and takes the party from the profile when the lookup has none", () => {
    render(<RepCard rep={house} profile={{ party: "D" }} />);
    expect(screen.getByText("House · CA-12")).toBeTruthy();
    expect(screen.getByText("Democrat")).toBeTruthy();
  });

  it("shows initials and no party until the profile loads", () => {
    const { container } = render(<RepCard rep={house} />);
    expect(container.querySelector("img")).toBeNull();
    expect(screen.getByText("AA").getAttribute("aria-hidden")).toBe("true");
    expect(screen.queryByText("Democrat")).toBeNull();
  });

  it("falls back to initials when the photo fails to load", () => {
    const { container } = render(<RepCard rep={rep} profile={{ party: "R", photoUrl: PHOTO }} />);
    fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector("img")).toBeNull();
    expect(screen.getByText("CC")).toBeTruthy();
  });
});

describe("RepCard score", () => {
  it("says how often they voted with you, with the counts beside the percentage", () => {
    viewport(true);
    render(<RepCard rep={rep} score={scored} />);
    expect(screen.getByText("Voted with you")).toBeTruthy();
    expect(screen.getByText("67%")).toBeTruthy();
    expect(screen.getByText("On 2 of the 3 bills you both voted yes or no on.")).toBeTruthy();
    expect(screen.getByText("1 more not counted (they didn't vote yes or no).")).toBeTruthy();
  });

  it("shows a dash, not 0%, when nothing is compared", () => {
    const none = { ...scored, matching: 0, compared: 0, member_absent: 1, alignment_pct: null, rows: [rows[2]] };
    const html = renderToStaticMarkup(<RepCard rep={rep} score={none} />);
    expect(html).toContain("—");
    expect(html).toContain("No bills in common with a yes or no vote yet.");
    expect(html).not.toContain("%");
  });

  it("before any votes, shows an empty score that says how it fills in", () => {
    render(<RepCard rep={rep} />);
    expect(screen.getByText("Fills in when you vote on a bill they voted on.")).toBeTruthy();
    expect(screen.queryByText("Bills compared")).toBeNull();
  });

  it.each([5, 50])("offers no Share with %i bills compared, nor space for one (#894)", (compared) => {
    viewport(true);
    const { container } = render(<RepCard rep={rep} score={{ ...scored, matching: 4, compared }} />);
    expect(screen.queryByRole("button", { name: /share/i })).toBeNull();
    expect(screen.queryByRole("link", { name: /share/i })).toBeNull();
    expect(container.querySelector(".mt-auto")).toBeNull();
  });

  it("says it's loading while the member's votes load", () => {
    render(<RepCard rep={rep} score="loading" />);
    expect(screen.getByRole("status", { name: "Loading Cora Chen's votes" })).toBeTruthy();
  });
});

describe("RepCard bills compared", () => {
  it("lists three bills on a wide screen, each with both votes and a spoken result, then Show all", () => {
    viewport(true);
    render(<RepCard rep={rep} score={scored} />);
    const list = screen.getByRole("list");
    const items = within(list).getAllByRole("listitem");
    expect(items).toHaveLength(3);
    expect(items[0].textContent).toContain("Match:");
    expect(items[0].textContent).toContain("You Yea");
    expect(items[1].textContent).toContain("No match:");
    expect(items[2].textContent).toContain("Not counted:");
    expect(items[2].textContent).toContain("They Not voting");
    expect(within(items[0]).getByRole("link", { name: "Bill One" }).getAttribute("href")).toBe("/bills/hr-119-1");

    const toggle = screen.getByRole("button", { name: "Show all 4 bills" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.getAttribute("aria-controls")).toBe(list.id);
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(within(list).getAllByRole("listitem")).toHaveLength(4);
    expect(screen.getByText("119th Congress · House")).toBeTruthy();
  });

  it("folds the bills away on a phone behind See the N bills compared", () => {
    viewport(false);
    render(<RepCard rep={rep} score={scored} />);
    expect(screen.queryByRole("list")).toBeNull();
    const toggle = screen.getByRole("button", { name: "See the 4 bills compared" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(toggle);
    expect(within(screen.getByRole("list")).getAllByRole("listitem")).toHaveLength(4);
    expect(screen.getByRole("button", { name: "Show fewer" }).getAttribute("aria-expanded")).toBe("true");
  });

  it("has no axe violations, folded or open", async () => {
    viewport(false);
    const { container } = render(<RepCard rep={rep} score={scored} profile={{ party: "R", photoUrl: PHOTO }} />);
    expect(await axeViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "See the 4 bills compared" }));
    expect(await axeViolations(container)).toEqual([]);
  });
});

describe("RepCard alignment line (#126)", () => {
  const alignment = {
    member_id: "C000003",
    congress: 119,
    scope_key: "CA",
    bills_compared: 22,
    bills_agreed: 14,
    computed_at: "2026-10-01T12:00:00Z",
  };

  it("says how often users in the visitor's state agreed, with the not-a-poll label", () => {
    const html = renderToStaticMarkup(<RepCard rep={rep} score={scored} alignment={alignment} />);
    expect(html).toContain("Just a Bill users in your state (CA) agreed with Cora Chen on 14 of 22 bills in");
    expect(html).toContain("119th Congress");
    expect(html).toContain("Not a poll.");
    expect(html).toContain('href="/methodology#aggregates"');
  });

  it("names a House member's district", () => {
    const html = renderToStaticMarkup(
      <RepCard
        rep={house}
        score={scored}
        alignment={{ ...alignment, scope_key: "CA-12", bills_compared: 1, bills_agreed: 1 }}
      />,
    );
    expect(html).toContain("users in your district (CA-12) agreed with Ada Alvarez on 1 of 1 bill in");
  });

  it("has no line without an alignment", () => {
    expect(renderToStaticMarkup(<RepCard rep={rep} score={scored} />)).not.toContain("Just a Bill users");
    expect(renderToStaticMarkup(<RepCard rep={rep} score={scored} alignment={null} />)).not.toContain(
      "Just a Bill users",
    );
  });
});

const reps: LocalReps = { state: "CA", district: 12, looked_up_at: "2026-09-30T00:00:00Z", members: [house, rep] };
const vote = { "s-119-5": { vote: "nay" as const, at: "2026-10-01T00:00:00Z" } };

function renderView(props: Partial<ScorecardViewProps>) {
  return render(<ScorecardView votes={{}} storage="device" reps={reps} scores={{ status: "none" }} {...props} />);
}

describe("ScorecardView", () => {
  it("shows a card per representative, with their profiles", () => {
    renderView({ profiles: { A000001: { party: "D" }, C000003: { party: "R", photoUrl: PHOTO } } });
    const cards = within(screen.getByRole("list")).getAllByRole("listitem");
    expect(cards.map((c) => within(c).getByRole("heading", { level: 3 }).textContent)).toEqual([
      "Ada Alvarez",
      "Cora Chen",
    ]);
    expect(within(cards[0]).getByText("Democrat")).toBeTruthy();
    expect(cards[1].querySelector("img")).not.toBeNull();
  });

  it("before any votes, leads into /vote instead of a dead end", () => {
    renderView({});
    expect(screen.getByRole("heading", { name: "Vote on a few bills to fill in your scorecard" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Start voting" }).getAttribute("href")).toBe("/vote");
    expect(screen.getAllByText("Fills in when you vote on a bill they voted on.")).toHaveLength(2);
  });

  it("with no representatives yet, offers the address form and a link to start voting now", () => {
    renderView({ reps: null });
    expect(screen.getByLabelText("Home address")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "How it works" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "start voting now" }).getAttribute("href")).toBe("/vote");
  });

  it("drops the vote banner once there are votes, and links the scorecard rule", () => {
    viewport(true);
    renderView({ votes: vote, scores: { status: "ready", scores: { C000003: scored } } });
    expect(screen.queryByRole("link", { name: "Start voting" })).toBeNull();
    expect(screen.getByRole("link", { name: "How the scorecard works" }).getAttribute("href")).toBe(
      "/methodology#scorecard",
    );
  });

  it("links to My votes below the cards, with the count and where they're kept (#739)", () => {
    renderView({ votes: vote });
    const link = screen.getByRole("link", { name: "My votes" });
    expect(link.getAttribute("href")).toBe("/my-votes");
    const line = link.closest("p")!;
    expect(line.textContent).toContain("1 vote so far, kept on this device only.");
    // After the cards in document order.
    expect(screen.getByRole("list").compareDocumentPosition(line) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("shows a retry when the votes didn't load", () => {
    const onRetry = vi.fn();
    renderView({ votes: vote, scores: { status: "error" }, onRetry });
    expect(screen.getByRole("alert").textContent).toContain("couldn't load");
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(onRetry).toHaveBeenCalledOnce();
  });

  it("shows the newest congress's alignment, or the one the switch picked (#126)", () => {
    const alignment = (congress: number, agreed: number) => ({
      member_id: "C000003",
      congress,
      scope_key: "CA",
      bills_compared: 10,
      bills_agreed: agreed,
      computed_at: "2026-10-01T12:00:00Z",
    });
    const view = (selected: number[]) =>
      renderToStaticMarkup(
        <ScorecardView
          votes={vote}
          storage="device"
          reps={{ ...reps, members: [rep] }}
          scores={{ status: "ready", scores: { C000003: scored } }}
          selected={selected}
          alignments={{ C000003: [alignment(119, 7), alignment(118, 4)] }}
        />,
      );
    expect(view([])).toContain("agreed with Cora Chen on 7 of 10 bills");
    expect(view([118])).toContain("agreed with Cora Chen on 4 of 10 bills");
  });

  it.each([
    ["no reps yet", { reps: null }],
    ["cards before any votes", {}],
    ["scored cards", { votes: vote, scores: { status: "ready", scores: { C000003: scored } } }],
  ] as const)("has no axe violations: %s", async (_, props) => {
    viewport(true);
    const { container } = renderView(props as Partial<ScorecardViewProps>);
    container.querySelector("details")?.setAttribute("open", "");
    expect(await axeViolations(container)).toEqual([]);
  });
});
