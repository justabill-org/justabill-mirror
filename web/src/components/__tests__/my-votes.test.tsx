// @vitest-environment jsdom
import type { ReactNode } from "react";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { VoteSection } from "@/app/(app)/bills/[id]/vote-section";
import { DeviceTools } from "@/components/votes/device-tools";
import { MyVotes } from "@/components/votes/my-votes";
import { ApiError } from "@/lib/api";
import type { LocalEnv } from "@/lib/local/storage";
import { createVoteStore, VOTES_KEY, type LocalVoteStore } from "@/lib/local/votes";
import type { BillStatuses } from "@/lib/my-votes";
import type { BillStatus, PaginatedResult, UserVote, UserVoteChoice } from "@/lib/types";
import { createAccountVoteBackend, type AccountVotesApi } from "@/lib/votes/account";
import { localVoteBackend, VOTE_CAP_REACHED, type VoteBackend } from "@/lib/votes/backend";
import { VoteBackendContext } from "@/lib/votes/hooks";
import { axeViolations } from "@/test/axe";

// My votes (#738, redesigned in #843): the summary, the filters, search and pages, where each bill
// stands, and changing, removing and undoing a vote, against the real local and account backends.

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
}));

// jsdom has no scrollIntoView; the page calls it when it turns a page of the list.
const scrollIntoView = vi.fn();
beforeAll(() => {
  Element.prototype.scrollIntoView = scrollIntoView;
});
afterAll(() => {
  delete (Element.prototype as Partial<Element>).scrollIntoView;
});

afterEach(cleanup);

function fakeEnv(): LocalEnv {
  const data = new Map<string, string>();
  return {
    storage: {
      getItem: (k) => data.get(k) ?? null,
      setItem: (k, v) => void data.set(k, v),
      removeItem: (k) => void data.delete(k),
    },
    events: new EventTarget(),
  };
}

type Seed = [billId: string, vote: UserVoteChoice, at: string, title?: string];

/** A browser store holding the seeded votes, each cast at its own time. */
function seededStore(seeds: Seed[], now = "2026-10-03T12:00:00.000Z"): LocalVoteStore {
  let clock = "";
  const store = createVoteStore({ env: fakeEnv, persist: () => undefined, now: () => new Date(clock || now) });
  for (const [id, vote, at, title] of seeds) {
    clock = at;
    store.setVote(id, vote, title);
  }
  clock = "";
  return store;
}

/** The local backend, with its server snapshot set to the client's so the list renders at once. */
function deviceBackend(store: LocalVoteStore, overrides: Partial<VoteBackend> = {}): VoteBackend {
  const local = localVoteBackend(store);
  return { ...local, getServerSnapshot: local.getSnapshot, ...overrides };
}

/** The 119th Congress's list as GET /bill-statuses would give it: only bills a chamber passed. */
const STATUSES_119: Record<string, BillStatus> = { "hr-119-1": "became_law", "s-119-1": "passed_senate" };

/** A status loader that answers at once with the lists of `congresses`. */
function statusesOf(byId: Record<string, BillStatus> = STATUSES_119, congresses = [119]) {
  const statuses: BillStatuses = { byId, congresses: new Set(congresses) };
  return () => Promise.resolve(statuses);
}

const NEVER = () => new Promise<BillStatuses>(() => {});

/** Renders the page's list and lets the status lists arrive. */
async function withBackend(
  backend: VoteBackend,
  child: ReactNode = <MyVotes loadStatuses={statusesOf()} />,
) {
  const view = render(<VoteBackendContext.Provider value={backend}>{child}</VoteBackendContext.Provider>);
  await act(async () => {});
  return view;
}

const SEEDS: Seed[] = [
  ["hr-119-1", "yea", "2026-10-01T12:00:00.000Z", "Companion Act"],
  ["s-119-1", "nay", "2026-10-02T12:00:00.000Z", "Companion Act"],
  ["hr-119-808", "skip", "2026-10-02T13:00:00.000Z", "Lamplight Library Hours Act"],
  ["hjres-119-7", "yea", "2026-10-03T12:00:00.000Z"],
];

function list(): HTMLElement {
  return screen.getByRole("list");
}

/** Each row's title link (the first link of the row), in order. */
function rowTitles(): string[] {
  return within(list())
    .getAllByRole("listitem")
    .map((li) => li.querySelector("a")?.textContent ?? "");
}

/** The row of a bill, by its href. */
function rowOf(billId: string): HTMLElement {
  const link = within(list())
    .getAllByRole("link")
    .find((a) => a.getAttribute("href") === `/bills/${billId}`);
  return link!.closest("li")!;
}

/** Opens a row's vote buttons. */
function openChange(row: HTMLElement) {
  fireEvent.click(within(row).getByRole("button", { name: /^Change my vote on / }));
}

function capError(): ApiError {
  const body = JSON.stringify({ error: "you can vote on at most 500 bills a day; try again later", code: "daily_vote_cap" });
  return new ApiError(429, "Too Many Requests", body);
}

/** Votes on `n` placeholder bills of the 119th, one a minute, titled "Act 1" (oldest) to "Act n". */
function manySeeds(n: number): Seed[] {
  return Array.from({ length: n }, (_, i) => [
    `hr-119-${i + 1}`,
    i % 2 ? "nay" : "yea",
    new Date(Date.UTC(2026, 8, 1, 0, i)).toISOString(),
    `Act ${i + 1}`,
  ]);
}

describe("the list", () => {
  it("shows every vote newest first with number, congress, linked title, status, the vote and the day", async () => {
    await withBackend(deviceBackend(seededStore(SEEDS)));

    // H.J.Res. 7 has no known title, so its number stands in.
    expect(rowTitles()).toEqual(["H.J.Res. 7", "Lamplight Library Hours Act", "Companion Act", "Companion Act"]);
    const senate = rowOf("s-119-1");
    expect(senate.textContent).toContain("S. 1 · 119th Congress");
    expect(senate.textContent).toContain("Passed Senate");
    expect(senate.textContent).toContain("Your vote: Nay");
    expect(senate.textContent).toContain("Voted Oct 2, 2026");
    expect(rowOf("hr-119-808").textContent).toContain("Your vote: Skipped");
  });

  it("summarizes the votes, how they split and how many became law, and says where they're kept", async () => {
    await withBackend(deviceBackend(seededStore(SEEDS)));
    expect(screen.getByRole("heading", { name: "4 votes" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Yea 2" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Nay 1" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Skipped 1" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Became law 1" })).toBeTruthy();
    expect(screen.getByText("Kept on this device only. Your votes aren't sent anywhere.")).toBeTruthy();
  });

  it("filters to a figure of the summary when it's pressed, and back when pressed again", async () => {
    await withBackend(deviceBackend(seededStore(SEEDS)));

    fireEvent.click(screen.getByRole("button", { name: "Yea 2" }));
    expect(rowTitles()).toEqual(["H.J.Res. 7", "Companion Act"]);
    expect(rowOf("hr-119-1")).toBeTruthy();
    expect(screen.getByText("2 of 4 votes")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Yea 2" }));
    expect(rowTitles()).toHaveLength(4);

    fireEvent.click(screen.getByRole("button", { name: "Became law 1" }));
    expect(rowTitles()).toEqual(["Companion Act"]);
    expect(rowOf("hr-119-1").textContent).toContain("Became law");
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(rowTitles()).toHaveLength(4);
  });

  it("puts a vote cast elsewhere (another tab, an import) at the top", async () => {
    const store = seededStore(SEEDS);
    await withBackend(deviceBackend(store));
    act(() => void store.setVote("hr-119-42", "nay", "Late Act"));
    expect(rowTitles()[0]).toBe("Late Act");
  });

  it("with no votes, says so and points to /vote", async () => {
    await withBackend(deviceBackend(seededStore([])));
    expect(screen.getByRole("heading", { name: "You haven't voted on any bills yet" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Start voting" }).getAttribute("href")).toBe("/vote");
    expect(screen.queryByRole("list")).toBeNull();
  });

  it("has no axe violations with votes, with a row open, or without votes", async () => {
    const { container } = await withBackend(deviceBackend(seededStore(manySeeds(12))));
    expect(await axeViolations(container)).toEqual([]);
    openChange(rowOf("hr-119-12"));
    expect(await axeViolations(container)).toEqual([]);
    cleanup();
    const empty = await withBackend(deviceBackend(seededStore([])));
    expect(await axeViolations(empty.container)).toEqual([]);
  });
});

describe("where each bill stands", () => {
  const OLD: Seed = ["hr-118-5", "nay", "2026-09-01T12:00:00.000Z", "Old Act"];

  it("says a listed bill's status, Not passed for its congress's other bills, and nothing it doesn't know", async () => {
    // The 118th's list isn't loaded (it failed), so its bill's status is unknown, not "Not passed".
    await withBackend(deviceBackend(seededStore([...SEEDS, OLD])));

    expect(rowOf("hr-119-1").textContent).toContain("Became law");
    expect(rowOf("hr-119-808").textContent).toContain("Not passed");
    expect(rowOf("hr-118-5").textContent).toContain("Status unavailable");
    expect(rowOf("hr-118-5").textContent).not.toContain("Not passed");
  });

  it("leaves the count of laws out when some status is unknown, and says so", async () => {
    await withBackend(deviceBackend(seededStore([...SEEDS, OLD])));
    expect(screen.queryByRole("button", { name: /^Became law/ })).toBeNull();
    expect(screen.getByText("Where some bills stand couldn't be loaded.")).toBeTruthy();
  });

  it("waits for the lists before offering Became law or the status menu, then fills them in", async () => {
    let answer: (s: BillStatuses) => void = () => {};
    const load = () => new Promise<BillStatuses>((resolve) => (answer = resolve));
    await withBackend(deviceBackend(seededStore(manySeeds(12))), <MyVotes loadStatuses={load} />);

    expect(screen.getByRole("button", { name: /^Became law/ })).toHaveProperty("disabled", true);
    expect(screen.getByLabelText("Where the bill stands")).toHaveProperty("disabled", true);
    expect(rowOf("hr-119-3").textContent).not.toContain("Status unavailable");

    await act(async () => answer({ byId: { "hr-119-3": "signed" }, congresses: new Set([119]) }));

    expect(screen.getByRole("button", { name: "Became law 1" })).toHaveProperty("disabled", false);
    const menu = screen.getByLabelText("Where the bill stands");
    expect(menu).toHaveProperty("disabled", false);
    expect(within(menu).getAllByRole("option").map((o) => o.textContent)).toEqual([
      "Any status",
      "Became law (1)",
      "Passed a chamber (0)",
      "Not passed (11)",
    ]);
    fireEvent.change(menu, { target: { value: "laws" } });
    expect(rowTitles()).toEqual(["Act 3"]);
  });

  it("links a bill a chamber passed to its Votes tab, and not one no chamber passed", async () => {
    await withBackend(deviceBackend(seededStore(SEEDS)));

    const passed = rowOf("s-119-1");
    openChange(passed);
    const link = within(passed).getByRole("link", { name: "How your representatives voted on S. 1" });
    expect(link.getAttribute("href")).toBe("/bills/s-119-1#votes");

    const notPassed = rowOf("hjres-119-7");
    openChange(notPassed);
    expect(within(notPassed).queryByRole("link", { name: /How your representatives voted/ })).toBeNull();
  });
});

describe("the filter bar", () => {
  it("is left out below 10 votes", async () => {
    await withBackend(deviceBackend(seededStore(SEEDS)));
    expect(screen.queryByRole("searchbox")).toBeNull();
  });

  it("narrows by title or bill number as you type, and says when nothing matches", async () => {
    await withBackend(deviceBackend(seededStore([...manySeeds(10), ...SEEDS.slice(2)])));
    const search = screen.getByLabelText("Search your votes by title or bill number");

    fireEvent.change(search, { target: { value: "lamplight" } });
    expect(rowTitles()).toEqual(["Lamplight Library Hours Act"]);

    // "hr 1" finds H.R. 1 and H.R. 10 by number, not H.J.Res. 7.
    fireEvent.change(search, { target: { value: "hr 1" } });
    expect(rowTitles()).toEqual(["Act 10", "Act 1"]);

    fireEvent.change(search, { target: { value: "farm bill" } });
    expect(screen.queryByRole("list")).toBeNull();
    expect(screen.getByText("None of your votes match these filters.")).toBeTruthy();
  });

  it("combines the vote and congress menus, each option counting what it would list", async () => {
    const seeds: Seed[] = [...manySeeds(10), ["hr-118-5", "nay", "2026-08-01T12:00:00.000Z", "Old Act"]];
    await withBackend(deviceBackend(seededStore(seeds)), <MyVotes loadStatuses={statusesOf({}, [118, 119])} />);

    const congress = screen.getByLabelText("Congress");
    expect(within(congress).getAllByRole("option").map((o) => o.textContent)).toEqual([
      "Every congress",
      "119th Congress (10)",
      "118th Congress (1)",
    ]);
    fireEvent.change(screen.getByLabelText("Your vote"), { target: { value: "nay" } });
    // Five of the 119th's votes are Nay, and the 118th's one.
    expect(within(congress).getAllByRole("option").map((o) => o.textContent)).toEqual([
      "Every congress",
      "119th Congress (5)",
      "118th Congress (1)",
    ]);
    fireEvent.change(congress, { target: { value: "118" } });
    expect(rowTitles()).toEqual(["Old Act"]);
    expect(screen.getByRole("button", { name: "Filters (2)" })).toBeTruthy();
  });

  it("shows 25 rows a page, the oldest on the next page, and goes to the top of the list", async () => {
    await withBackend(deviceBackend(seededStore(manySeeds(26))));

    expect(rowTitles()).toHaveLength(25);
    expect(rowTitles()[0]).toBe("Act 26");
    expect(rowTitles()).not.toContain("Act 1");

    scrollIntoView.mockClear();
    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    expect(rowTitles()).toEqual(["Act 1"]);
    expect(scrollIntoView).toHaveBeenCalledWith({ block: "start" });
    fireEvent.click(screen.getByRole("button", { name: "Previous page" }));
    expect(rowTitles()[0]).toBe("Act 26");
  });
});

describe("changing a vote", () => {
  it("saves the new choice, keeps the row in place, and the bill page shows it without a reload", async () => {
    const store = seededStore(SEEDS);
    await withBackend(
      deviceBackend(store),
      <>
        <MyVotes loadStatuses={statusesOf()} />
        <VoteSection billId="hr-119-1" billTitle="Companion Act" />
      </>,
    );
    const billCard = screen.getByText("Your Vote").closest("div")!.parentElement!;
    const row = rowOf("hr-119-1");
    expect(within(list()).getAllByRole("listitem")[3]).toBe(row);

    openChange(row);
    await act(async () => fireEvent.click(within(row).getByRole("button", { name: "Nay" })));

    expect(store.getSnapshot().value["hr-119-1"]).toMatchObject({ vote: "nay", title: "Companion Act" });
    expect(within(row).getByRole("button", { name: "Nay" }).getAttribute("aria-pressed")).toBe("true");
    expect(row.textContent).toContain("Your vote: Nay");
    expect(row.textContent).toContain("Voted Oct 3, 2026");
    // Newest first would move it to the top; it stays put until the filters change.
    expect(within(list()).getAllByRole("listitem")[3]).toBe(row);
    expect(within(billCard).getByRole("button", { name: "Nay" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("button", { name: "Nay 2" })).toBeTruthy();
  });

  it("keeps a changed row under a filter it no longer matches until the filters change", async () => {
    await withBackend(deviceBackend(seededStore(SEEDS)));
    fireEvent.click(screen.getByRole("button", { name: "Yea 2" }));
    const row = rowOf("hr-119-1");
    openChange(row);
    await act(async () => fireEvent.click(within(row).getByRole("button", { name: "Nay" })));
    expect(rowTitles()).toEqual(["H.J.Res. 7", "Companion Act"]);

    fireEvent.click(screen.getByRole("button", { name: "Yea 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Yea 1" }));
    expect(rowTitles()).toEqual(["H.J.Res. 7"]);
  });

  it("keeps the stored vote and says which one failed when a save fails, and Try again resends it", async () => {
    const store = seededStore([SEEDS[0]]);
    const local = deviceBackend(store);
    let failures = 1;
    const setVote = vi.fn(async (id: string, vote: UserVoteChoice, title?: string) => {
      if (failures-- > 0) throw new Error("offline");
      await local.setVote(id, vote, title);
    });
    await withBackend({ ...local, setVote });
    const row = rowOf("hr-119-1");
    openChange(row);

    await act(async () => fireEvent.click(within(row).getByRole("button", { name: "Nay" })));

    expect(screen.getByRole("alert").textContent).toContain("Your Nay vote couldn't be saved.");
    expect(store.getSnapshot().value["hr-119-1"].vote).toBe("yea");
    expect(within(row).getByRole("button", { name: "Yea" }).getAttribute("aria-pressed")).toBe("true");

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Try again" })));
    expect(setVote).toHaveBeenLastCalledWith("hr-119-1", "nay", "Companion Act");
    expect(screen.queryByRole("alert")).toBeNull();
    expect(store.getSnapshot().value["hr-119-1"].vote).toBe("nay");
  });

  it("says the daily limit is reached, with no Try again, and leaves the vote as it was", async () => {
    const store = seededStore([SEEDS[0]]);
    await withBackend(deviceBackend(store, { setVote: () => Promise.reject(capError()) }));
    const row = rowOf("hr-119-1");
    openChange(row);

    await act(async () => fireEvent.click(within(row).getByRole("button", { name: "Nay" })));

    expect(screen.getByRole("alert").textContent).toBe(VOTE_CAP_REACHED);
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
    expect(store.getSnapshot().value["hr-119-1"].vote).toBe("yea");
  });
});

describe("removing a vote", () => {
  it("removes it from the store, offers Undo in its place, and Undo casts it again", async () => {
    const store = seededStore(SEEDS);
    await withBackend(deviceBackend(store));
    const row = rowOf("hjres-119-7");
    openChange(row);

    await act(async () => fireEvent.click(within(row).getByRole("button", { name: "Remove my vote on H.J.Res. 7" })));

    expect(store.getSnapshot().value["hjres-119-7"]).toBeUndefined();
    const note = within(row).getByRole("status");
    expect(note.textContent).toContain("Removed your vote on H.J.Res. 7.");
    expect(document.activeElement).toBe(note);
    expect(row.textContent).toContain("Your vote: Removed");
    expect(screen.getByRole("heading", { name: "3 votes" })).toBeTruthy();

    await act(async () => fireEvent.click(within(row).getByRole("button", { name: "Undo removing my vote on H.J.Res. 7" })));

    expect(store.getSnapshot().value["hjres-119-7"].vote).toBe("yea");
    expect(within(row).getByRole("button", { name: "Yea" }).getAttribute("aria-pressed")).toBe("true");
    expect(document.activeElement).toBe(within(row).getByRole("button", { name: "Done changing my vote on H.J.Res. 7" }));
  });

  it("keeps the last vote's row to undo rather than showing the empty page", async () => {
    await withBackend(deviceBackend(seededStore([SEEDS[0]])));
    openChange(rowOf("hr-119-1"));
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Remove my vote on H.R. 1" })));
    expect(within(rowOf("hr-119-1")).getByRole("status").textContent).toContain("Removed your vote on H.R. 1.");
    expect(screen.queryByText("You haven't voted on any bills yet")).toBeNull();
  });

  it("says the removal failed and keeps the vote when it fails", async () => {
    const store = seededStore([SEEDS[0]]);
    await withBackend(deviceBackend(store, { clearVote: () => Promise.reject(new Error("offline")) }));
    const row = rowOf("hr-119-1");
    openChange(row);

    await act(async () => fireEvent.click(within(row).getByRole("button", { name: "Remove my vote on H.R. 1" })));

    expect(screen.getByRole("alert").textContent).toContain("Your vote couldn't be removed.");
    expect(store.getSnapshot().value["hr-119-1"].vote).toBe("yea");
    expect(row.textContent).toContain("Your vote: Yea");
  });
});

describe("signed in", () => {
  function accountApi(items: UserVote[], overrides: Partial<AccountVotesApi> = {}) {
    const api = {
      getMyVotes: vi.fn(
        async (): Promise<PaginatedResult<UserVote>> => ({ items, total: items.length, offset: 0, limit: 100 }),
      ),
      castVote: vi.fn(async () => ({})),
      deleteVote: vi.fn(async () => undefined),
      ...overrides,
    };
    const backend = createAccountVoteBackend({
      getIdToken: async () => "token-1",
      api,
      now: () => new Date("2026-10-03T12:00:00.000Z"),
    });
    return { api, backend };
  }

  const ROWS: UserVote[] = [
    { user_id: "u-1", bill_id: "s-119-1", vote: "nay", voted_at: "2026-10-02T12:00:00Z", title: "Companion Act" },
    { user_id: "u-1", bill_id: "hr-119-1", vote: "yea", voted_at: "2026-10-01T12:00:00Z", title: "Companion Act" },
  ];

  it("lists the account's votes with their statuses and sends a change and a removal to the API", async () => {
    const { api, backend } = accountApi(ROWS);
    await withBackend(backend);
    expect(await screen.findByText(/^Saved in your account\./)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Settings" }).getAttribute("href")).toBe("/settings");
    expect(screen.getByRole("button", { name: "Became law 1" })).toBeTruthy();

    const row = rowOf("hr-119-1");
    openChange(row);
    await act(async () => fireEvent.click(within(row).getByRole("button", { name: "Nay" })));
    expect(api.castVote).toHaveBeenCalledWith("token-1", "hr-119-1", "nay");
    expect(within(row).getByRole("button", { name: "Nay" }).getAttribute("aria-pressed")).toBe("true");

    await act(async () => fireEvent.click(within(row).getByRole("button", { name: "Remove my vote on H.R. 1" })));
    expect(api.deleteVote).toHaveBeenCalledWith("token-1", "hr-119-1");
    expect(within(row).getByRole("status").textContent).toContain("Removed your vote on H.R. 1.");
  });

  it("has no device tools: the votes are in the account", async () => {
    const { backend } = accountApi(ROWS);
    await withBackend(backend, <MyVotes deviceStore={seededStore(SEEDS)} loadStatuses={statusesOf()} />);
    expect(await screen.findByText(/^Saved in your account\./)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Import votes" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Clear my votes" })).toBeNull();
  });

  it("shows a loading skeleton while the account's votes load", async () => {
    const { backend } = accountApi([], { getMyVotes: () => new Promise(() => {}) });
    await withBackend(backend, <MyVotes loadStatuses={NEVER} />);
    expect(screen.getByRole("status", { name: "Loading your votes" })).toBeTruthy();
    expect(screen.queryByRole("list")).toBeNull();
  });

  it("says the votes couldn't be loaded, and Try again loads them", async () => {
    const getMyVotes = vi
      .fn<AccountVotesApi["getMyVotes"]>()
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue({ items: ROWS, total: 2, offset: 0, limit: 100 });
    const { backend } = accountApi([], { getMyVotes });
    vi.spyOn(console, "error").mockImplementation(() => {});
    await withBackend(backend);

    expect((await screen.findByRole("alert")).textContent).toBe("We couldn't load your votes from your account.");
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Try again" })));
    expect(await screen.findByText(/^Saved in your account\./)).toBeTruthy();
    expect(getMyVotes).toHaveBeenCalledTimes(2);
  });
});

// The device tools moved here from the scorecard (#739): download, import, clear and the unreadable
// votes' download, all in the browser.
describe("the device tools, signed out", () => {
  /** A votes file as "Download my votes" writes it. */
  function votesFile(votes: { bill_id: string; vote: UserVoteChoice; voted_at: string; title?: string }[]): File {
    const text = JSON.stringify({ v: 1, exported_at: "2026-10-03T12:00:00.000Z", votes });
    return new File([text], "just-a-bill-votes.json", { type: "application/json" });
  }

  async function importFile(file: File) {
    await act(async () => fireEvent.change(screen.getByLabelText("Votes file to import"), { target: { files: [file] } }));
  }

  function withDevice(store: LocalVoteStore) {
    return withBackend(deviceBackend(store), <MyVotes deviceStore={store} loadStatuses={statusesOf()} />);
  }

  it("downloads this device's votes, newest first, and sends nothing", () => {
    const fetchSpy = vi.fn();
    vi.stubGlobal("fetch", fetchSpy);
    const save = vi.fn();
    render(<DeviceTools store={seededStore(SEEDS)} save={save} />);

    fireEvent.click(screen.getByRole("button", { name: "Download my votes" }));

    expect(save).toHaveBeenCalledTimes(1);
    const [text, filename] = save.mock.calls[0];
    expect(filename).toBe("just-a-bill-votes.json");
    expect(JSON.parse(text).votes.map((v: { bill_id: string }) => v.bill_id)).toEqual([
      "hjres-119-7",
      "hr-119-808",
      "s-119-1",
      "hr-119-1",
    ]);
    expect(fetchSpy).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  it("imports a file on a device with no votes: the list appears newest first and says what was imported", async () => {
    const store = seededStore([]);
    await withDevice(store);
    expect(screen.getByRole("heading", { name: "You haven't voted on any bills yet" })).toBeTruthy();

    await importFile(
      votesFile([
        { bill_id: "hr-119-1", vote: "yea", voted_at: "2026-09-01T12:00:00.000Z", title: "Older Act" },
        { bill_id: "s-119-2", vote: "nay", voted_at: "2026-09-02T12:00:00.000Z", title: "Newer Act" },
      ])
    );

    expect(rowTitles()).toEqual(["Newer Act", "Older Act"]);
    expect(store.getSnapshot().value["s-119-2"].vote).toBe("nay");
    expect(screen.getByText("Imported 2 new and 0 updated votes (0 unchanged).")).toBeTruthy();
  });

  it("puts a vote the import made newer in its place in the order", async () => {
    await withDevice(seededStore(SEEDS));
    expect(rowTitles()).toEqual(["H.J.Res. 7", "Lamplight Library Hours Act", "Companion Act", "Companion Act"]);

    await importFile(votesFile([{ bill_id: "hr-119-1", vote: "nay", voted_at: "2026-10-03T13:00:00.000Z" }]));

    // H.R. 1 was the oldest; the file's vote on it is the newest.
    expect(rowTitles()).toEqual(["Companion Act", "H.J.Res. 7", "Lamplight Library Hours Act", "Companion Act"]);
    const first = within(list()).getAllByRole("listitem")[0];
    expect(first.querySelector("a")!.getAttribute("href")).toBe("/bills/hr-119-1");
    expect(first.textContent).toContain("Your vote: Nay");
    expect(screen.getByText("Imported 0 new and 1 updated votes (0 unchanged).")).toBeTruthy();
  });

  it("says why a file can't be imported and keeps the votes as they were", async () => {
    const store = seededStore(SEEDS);
    await withDevice(store);
    const before = store.getSnapshot().value;

    await importFile(new File(["not json"], "votes.json", { type: "application/json" }));

    expect(screen.getByText("That file isn't valid JSON.")).toBeTruthy();
    expect(store.getSnapshot().value).toEqual(before);
    expect(rowTitles()).toHaveLength(4);
  });

  it("clears every vote only after the visitor confirms", async () => {
    const store = seededStore(SEEDS);
    await withDevice(store);
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);

    fireEvent.click(screen.getByRole("button", { name: "Clear my votes" }));
    expect(Object.keys(store.getSnapshot().value)).toHaveLength(4);
    expect(rowTitles()).toHaveLength(4);

    fireEvent.click(screen.getByRole("button", { name: "Clear my votes" }));
    expect(store.getSnapshot().value).toEqual({});
    expect(screen.getByRole("heading", { name: "You haven't voted on any bills yet" })).toBeTruthy();
    expect(screen.getByText("Your votes on this device were cleared.")).toBeTruthy();
    expect(confirm).toHaveBeenCalledTimes(2);
    confirm.mockRestore();
  });

  it("offers saved votes that couldn't be read for download", () => {
    const env = fakeEnv();
    env.storage!.setItem(VOTES_KEY, "{oops");
    const store = createVoteStore({ env: () => env, persist: () => undefined });
    const save = vi.fn();
    render(<DeviceTools store={store} save={save} />);

    fireEvent.click(screen.getByRole("button", { name: "Download them" }));
    expect(save).toHaveBeenCalledWith("{oops", "just-a-bill-votes-unreadable.json");
  });

  it("doesn't mention unreadable votes when there are none", () => {
    render(<DeviceTools store={seededStore(SEEDS)} save={vi.fn()} />);
    expect(screen.queryByRole("button", { name: "Download them" })).toBeNull();
  });
});
