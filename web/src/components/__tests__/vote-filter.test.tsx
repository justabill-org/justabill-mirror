// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { AuthProvider } from "@/lib/auth/provider";
import { billList } from "@/lib/examples";
import type { LocalEnv } from "@/lib/local/storage";
import { createVoteStore } from "@/lib/local/votes";
import type { Bill, BillCardItem, Congress, PaginatedResult } from "@/lib/types";
import { localVoteBackend, type VoteBackend } from "@/lib/votes/backend";
import { VoteBackendContext } from "@/lib/votes/hooks";
import { axeViolations } from "@/test/axe";
import { fakeAuthStore } from "@/test/fake-auth";

// #797: /vote takes the /bills filters from the URL and deals every bill under them, reading the
// list from the API in the browser past the batch the page rendered.

let search = "";
const push = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", () => ({
  usePathname: () => "/vote",
  useRouter: () => ({ push }),
  useSearchParams: () => new URLSearchParams(search),
}));

vi.mock("@/lib/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api")>()),
  listBills: vi.fn(),
  listBillsWithCards: vi.fn(),
}));

const api = await import("@/lib/api");
const { FilteredDeck } = await import("@/app/(app)/vote/filtered-deck");

const congresses: Congress[] = [
  { number: 118, start_date: "2023-01-03", is_current: false },
  { number: 119, start_date: "2025-01-03", is_current: true },
];
const policyAreas = ["Agriculture and Food", "Health"];

/** A bill as `GET /bills?include=summary,card` sends it, with no summary and no passage yet. */
function bill(n: number): BillCardItem {
  const b: Bill = { ...billList.items[0], id: `hr-119-${n}`, congress: 119, number: n, title: `Bill ${n}` };
  return { ...b, summary: null, card: { crs: null, passage: [], enacted: null, law_change_count: 0 } };
}

function pageOf(ns: number[], total = ns.length): PaginatedResult<BillCardItem> {
  return { items: ns.map(bill), total, offset: 0, limit: 20 };
}

/** The default deck the page rendered: three laws. */
const first = pageOf([1, 2, 3]);

function memoryBackend(): VoteBackend {
  const data = new Map<string, string>();
  const env: LocalEnv = {
    storage: {
      getItem: (k) => data.get(k) ?? null,
      setItem: (k, v) => void data.set(k, v),
      removeItem: (k) => void data.delete(k),
    },
    events: new EventTarget(),
  };
  const local = localVoteBackend(createVoteStore({ env: () => env, persist: () => undefined }));
  return { ...local, getServerSnapshot: local.getSnapshot };
}

function renderDeck(backend: VoteBackend = memoryBackend(), wrap: (ui: ReactNode) => ReactNode = (ui) => ui) {
  return render(
    wrap(
      <VoteBackendContext.Provider value={backend}>
        <FilteredDeck congresses={congresses} policyAreas={policyAreas} first={first} />
      </VoteBackendContext.Provider>
    )
  );
}

const filterButton = () => screen.getByRole("button", { name: /^Filter:/ });
const cardTitle = () => screen.getByRole("heading", { level: 2 }).textContent;

beforeEach(() => {
  vi.mocked(api.listBillsWithCards).mockResolvedValue(pageOf([]));
  vi.mocked(api.listBills).mockResolvedValue({ items: [], total: 0, offset: 0, limit: 1 });
});

afterEach(() => {
  cleanup();
  search = "";
  push.mockReset();
  vi.mocked(api.listBillsWithCards).mockReset();
  vi.mocked(api.listBills).mockReset();
});

describe("/vote with no query", () => {
  it("deals the rendered Laws batch without reading it again, and says what the deck holds", () => {
    renderDeck();
    expect(cardTitle()).toBe("Bill 1");
    expect(screen.getByText("Laws: 3 of 3 bills left")).toBeTruthy();
    expect(screen.getByText("0 voted this visit")).toBeTruthy();
    expect(screen.queryByText(/ of 10/)).toBeNull();
    expect(screen.getByRole("link", { name: "See as a list" }).getAttribute("href")).toBe("/bills");
    expect(filterButton().getAttribute("aria-expanded")).toBe("false");
    expect(api.listBillsWithCards).not.toHaveBeenCalled();
  });

  it("removes the filter an older version kept on the device", () => {
    localStorage.setItem("jab.vote-filter.v1", '{"stage":"chamber","topic":"Health"}');
    localStorage.setItem("jab.votes.v1", "{}");
    renderDeck();
    expect(localStorage.getItem("jab.vote-filter.v1")).toBeNull();
    expect(localStorage.getItem("jab.votes.v1")).toBe("{}");
    expect(cardTitle()).toBe("Bill 1");
  });
});

describe("/vote's filter", () => {
  it("offers what /bills offers: the four views, search, sort, policy area, congress, type and chamber", () => {
    renderDeck();
    fireEvent.click(filterButton());
    expect(filterButton().getAttribute("aria-expanded")).toBe("true");
    const views = screen.getAllByRole("radio").map((r) => r.closest("label")?.textContent);
    expect(views).toEqual(["Laws", "Passed a chamber", "In committee", "All"]);
    expect((screen.getByRole("radio", { name: "Laws" }) as HTMLInputElement).checked).toBe(true);
    for (const label of ["Search bills", "Sort by", "Policy area", "Congress", "Bill type", "Chamber of origin"]) {
      expect(screen.getByLabelText(label)).toBeTruthy();
    }
    // The deck is always unvoted, so the /bills checkbox isn't offered.
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(screen.queryByRole("button", { name: "Clear filters" })).toBeNull();
  });

  it("puts each choice in the URL as a new history entry, keeping the others", () => {
    search = "show=passed";
    renderDeck();
    fireEvent.click(filterButton());
    fireEvent.change(screen.getByLabelText("Bill type"), { target: { value: "hr" } });
    expect(push).toHaveBeenLastCalledWith("/vote?show=passed&type=hr", { scroll: false });
    fireEvent.click(screen.getByRole("radio", { name: "Laws" }));
    expect(push).toHaveBeenLastCalledWith("/vote", { scroll: false });
    fireEvent.change(screen.getByLabelText("Policy area"), { target: { value: "Health" } });
    expect(push).toHaveBeenLastCalledWith("/vote?show=passed&area=Health", { scroll: false });
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(push).toHaveBeenLastCalledWith("/vote", { scroll: false });
  });

  it("searches once the typing stops", async () => {
    renderDeck();
    fireEvent.click(filterButton());
    fireEvent.change(screen.getByLabelText("Search bills"), { target: { value: "water rights" } });
    expect(push).not.toHaveBeenCalled();
    await waitFor(() => expect(push).toHaveBeenCalledWith("/vote?q=water+rights", { scroll: false }));
  });

  it("names the filters on the closed button and marks a choice other than the default", () => {
    search = "show=committee&congress=118&type=hr&area=Health";
    renderDeck();
    expect(filterButton().getAttribute("aria-label")).toBe(
      "Filter: In committee, Health, 118th Congress, H.R."
    );
    expect(filterButton().querySelector("span[aria-hidden]")).not.toBeNull();
    cleanup();
    search = "";
    renderDeck();
    expect(filterButton().getAttribute("aria-label")).toBe("Filter: Laws");
    expect(filterButton().querySelector("span[aria-hidden]")).toBeNull();
  });
});

describe("/vote under other filters", () => {
  it("reads the /bills list for them from the API, signed out with nothing about the user's votes", async () => {
    search = "show=passed&type=hr";
    vi.mocked(api.listBillsWithCards).mockResolvedValue(pageOf([7, 8], 42));
    renderDeck();
    expect(await screen.findByRole("heading", { level: 2, name: "Bill 7" })).toBeTruthy();
    expect(vi.mocked(api.listBillsWithCards).mock.calls).toEqual([
      [
        {
          congress: 119,
          sort: "latest_action",
          type: "hr",
          status: ["passed_house", "passed_senate", "resolving_differences", "to_president", "vetoed"],
          offset: 0,
          limit: 20,
        },
      ],
    ]);
    expect(api.listBills).not.toHaveBeenCalled();
    expect(screen.getByText("Passed a chamber: 42 of 42 bills left")).toBeTruthy();
    expect(screen.getByRole("link", { name: "See as a list" }).getAttribute("href")).toBe(
      "/bills?show=passed&type=hr"
    );
  });

  it("says no bills are found when nothing matches, with a way to clear the filters", async () => {
    search = "area=Nope";
    renderDeck();
    expect(await screen.findByRole("heading", { name: "No bills found" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Clear filters" }).getAttribute("href")).toBe("/vote");
    expect(screen.queryByText(/bills left/)).toBeNull();
  });

  it("says a batch couldn't be read and keeps the filter; Try again reads it", async () => {
    search = "show=all";
    vi.mocked(api.listBillsWithCards).mockRejectedValueOnce(new TypeError("Failed to fetch"));
    renderDeck();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("We couldn't load more bills.");
    vi.mocked(api.listBillsWithCards).mockResolvedValue(pageOf([5]));
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("heading", { level: 2, name: "Bill 5" })).toBeTruthy();
    expect(push).not.toHaveBeenCalled();
  });

  it("ends with Change filters, My votes and Scorecard; Change filters opens the filter", async () => {
    search = "show=committee";
    const backend = memoryBackend();
    await backend.setVote("hr-119-9", "yea", "Bill 9");
    vi.mocked(api.listBillsWithCards).mockResolvedValue(pageOf([9]));
    renderDeck(backend);
    expect(await screen.findByRole("heading", { name: "You've voted on every bill here" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "My votes" }).getAttribute("href")).toBe("/my-votes");
    expect(screen.getByRole("link", { name: "Scorecard" }).getAttribute("href")).toBe("/scorecard");
    vi.spyOn(window, "scrollTo").mockImplementation(() => undefined);
    fireEvent.click(screen.getByRole("button", { name: "Change filters" }));
    expect(filterButton().getAttribute("aria-expanded")).toBe("true");
  });
});

describe("/vote signed in", () => {
  it("reads only the bills the user hasn't voted on, with their token, and the list's total for everyone", async () => {
    search = "show=passed";
    const state = { votes: {}, storage: "account" as const };
    const account: VoteBackend = {
      subscribe: () => () => {},
      getSnapshot: () => state,
      getServerSnapshot: () => state,
      setVote: vi.fn(async () => {}),
      clearVote: vi.fn(async () => {}),
    };
    vi.mocked(api.listBills).mockResolvedValue({ items: [], total: 50, offset: 0, limit: 1 });
    vi.mocked(api.listBillsWithCards).mockResolvedValue(pageOf([4], 12));
    const auth = fakeAuthStore("signed-in");
    renderDeck(account, (ui) => <AuthProvider store={auth.store}>{ui}</AuthProvider>);

    expect(await screen.findByRole("heading", { level: 2, name: "Bill 4" })).toBeTruthy();
    const statuses = ["passed_house", "passed_senate", "resolving_differences", "to_president", "vetoed"];
    expect(vi.mocked(api.listBillsWithCards).mock.calls).toEqual([
      [{ congress: 119, sort: "latest_action", status: statuses, unvoted: true, offset: 0, limit: 20 }, "id-token-1"],
    ]);
    // The count is the shared list's, sent without the token.
    expect(vi.mocked(api.listBills).mock.calls).toEqual([
      [{ congress: 119, sort: "latest_action", status: statuses, limit: 1 }],
    ]);
    expect(screen.getByText("Passed a chamber: 12 of 50 bills left")).toBeTruthy();
  });
});

describe("/vote accessibility", () => {
  it("has no axe violations closed or open", async () => {
    const { container } = renderDeck();
    expect(await axeViolations(container)).toEqual([]);
    await act(async () => fireEvent.click(filterButton()));
    expect(await axeViolations(container)).toEqual([]);
  });
});
