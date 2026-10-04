// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { renderToString } from "react-dom/server";
import type { Bill, BillDetailResponse, Congress } from "@/lib/types";

// Review focus (#870): a signed-out visitor with votes on the device opens /vote with a /bills
// filter that matches nothing, through the real URL parsing, list query, local vote store and deck.

let search = "";
vi.mock("next/navigation", () => ({
  usePathname: () => "/vote",
  useRouter: () => ({ push: vi.fn() }),
  useSearchParams: () => new URLSearchParams(search),
}));
vi.mock("@/lib/obs/browser", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  reportError: vi.fn(),
}));

const { FilteredDeck } = await import("@/app/(app)/vote/filtered-deck");
const { SwipeCard } = await import("@/components/vote/swipe-card");

const congresses: Congress[] = [
  { number: 118, start_date: "2023-01-03", is_current: false },
  { number: 119, start_date: "2025-01-03", is_current: true },
];

afterEach(() => {
  cleanup();
  search = "";
  vi.unstubAllGlobals();
});

describe("signed out, votes on the device, a filter matching no bill", () => {
  it("hydrates from the skeleton to an honest empty state, sending nothing about the votes", async () => {
    localStorage.setItem(
      "jab.votes.v1",
      JSON.stringify({ v: 1, votes: { "hr-119-1": { vote: "yea", at: "2026-10-01T00:00:00.000Z", title: "A" } } })
    );
    search = "show=all&type=hr&area=No+Such+Area&q=zzzz";
    const fetchMock = vi.fn<(url: string) => Promise<Response>>(
      async () => new Response(JSON.stringify({ items: [], total: 0, offset: 0, limit: 20 }), { status: 200 })
    );
    vi.stubGlobal("fetch", fetchMock);

    const ui = <FilteredDeck congresses={congresses} policyAreas={["Health"]} />;
    // The server HTML (the store's server snapshot): the loading skeleton.
    const container = document.createElement("div");
    container.innerHTML = renderToString(ui);
    document.body.appendChild(container);
    expect(container.querySelector('[aria-label="Loading bills"]')).not.toBeNull();

    render(ui, { container, hydrate: true });
    expect(await screen.findByRole("heading", { name: "No bills found" })).toBeTruthy();
    expect(screen.getByText("No bill matches these filters.")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Clear filters" }).getAttribute("href")).toBe("/vote");
    expect(screen.queryByRole("status", { name: "Loading bills" })).toBeNull();
    expect(screen.queryByText(/bills left/)).toBeNull();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const url = new URL(String(fetchMock.mock.calls[0][0]));
    expect(url.pathname).toBe("/api/v1/bills");
    expect(Object.fromEntries(url.searchParams)).toEqual({
      offset: "0",
      limit: "20",
      congress: "119",
      type: "hr",
      policy_area: "No Such Area",
      q: "zzzz",
      sort: "latest_action",
      include: "summary,card",
    });
    // Nothing about the device's votes leaves the browser.
    expect(String(fetchMock.mock.calls[0][0])).not.toContain("hr-119-1");
    expect(String(fetchMock.mock.calls[0][0])).not.toContain("unvoted");
    expect(localStorage.getItem("jab.votes.v1")).toContain("hr-119-1");
  });
});

describe("a /vote card for a bill with no CRS summary, no AI summary and no text", () => {
  const bill = {
    id: "hr-119-77",
    congress: 119,
    bill_type: "hr",
    number: 77,
    title: "Bare Act",
    current_status: "in_committee",
    status_date: null,
    introduced_date: "2025-03-01T00:00:00Z",
    sponsors: [],
  } as unknown as Bill;

  it("degrades every section on the card and in Details", async () => {
    const detail = {
      bill,
      actions: [],
      summary: null,
      crs_summary: null,
      text_versions: [],
      diffs: [],
      amendments: [],
      votes: [],
      status_history: [],
      gao_reports: [],
      sponsorships: [],
    } as unknown as BillDetailResponse;
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify(detail), { status: 200 }))
    );

    render(<SwipeCard bill={bill} summary={null} card={null} onVote={() => {}} />);
    expect(screen.getByText("No summary of this bill yet. Details links to its official record.")).toBeTruthy();
    expect(screen.queryByText(/Who it affects/)).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Details" }));
    expect(await screen.findByText(/This bill has no summary yet/)).toBeTruthy();
    expect(screen.queryByRole("status", { name: "Loading more about this bill" })).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByText("Introduced Mar 1, 2025")).toBeTruthy();
    expect(screen.queryByText("What it changes in law")).toBeNull();
    expect(screen.queryByText("How Congress voted")).toBeNull();
    expect(screen.queryByText("Who backed it")).toBeNull();
  });
});
