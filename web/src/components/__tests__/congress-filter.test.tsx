// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { LocalScorecard } from "@/components/scorecard/local-scorecard";
import type { LocalEnv } from "@/lib/local/storage";
import { createVoteStore } from "@/lib/local/votes";
import { localVoteBackend, type VoteBackend } from "@/lib/votes/backend";
import { localReps, VoteBackendContext } from "@/lib/votes/hooks";

// #243: the congress switch narrows what /scorecard compares (/vote's congress filter: vote-filter.test.tsx);
// #844 shows it only when the visitor has votes in two congresses, under "Your representatives".

let search = "";
const push = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", () => ({
  usePathname: () => "/scorecard",
  useRouter: () => ({ push }),
  useSearchParams: () => new URLSearchParams(search),
}));

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

async function backendWith(votes: Record<string, "yea" | "nay">): Promise<VoteBackend> {
  const env = fakeEnv();
  const backend = localVoteBackend(createVoteStore({ env: () => env, persist: () => undefined }));
  for (const [billId, vote] of Object.entries(votes)) await backend.setVote(billId, vote, `Title of ${billId}`);
  return backend;
}

function withBackend(backend: VoteBackend, child: ReactNode) {
  return render(<VoteBackendContext.Provider value={backend}>{child}</VoteBackendContext.Provider>);
}

const offered = [119, 118];

afterEach(() => {
  cleanup();
  search = "";
  push.mockReset();
});

describe("LocalScorecard with the congress switch", () => {
  let urls: string[];

  beforeEach(() => {
    urls = [];
    localReps().saveReps({
      state: "CA",
      district: 30,
      looked_up_at: "2026-10-01T00:00:00.000Z",
      members: [{ id: "S001150", name: "Adam Schiff", party: "", chamber: "Senate", state: "CA" }],
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string | URL) => {
        const url = String(input);
        urls.push(url.replace(/^.*\/api\/v1/, ""));
        const congress = Number(/congress=(\d+)/.exec(url)?.[1]);
        const [billId, chamber] = congress === 118 ? ["hr-118-5", "House"] : ["s-119-7", "Senate"];
        return Response.json({
          member_id: "S001150",
          congress,
          rule: "final-passage-v1",
          positions: [{ bill_id: billId, vote: "yea", vote_id: `v-${congress}`, chamber, vote_date: "2024-05-01T00:00:00Z" }],
        });
      })
    );
  });
  afterEach(() => {
    localReps().clearReps();
    vi.unstubAllGlobals();
  });

  it("compares every congress voted in by default (All)", async () => {
    withBackend(await backendWith({ "hr-118-5": "yea", "s-119-7": "nay" }), <LocalScorecard offered={offered} />);

    expect(await screen.findByText("Bills compared")).toBeTruthy();
    expect(urls.sort()).toEqual([
      "/members/S001150",
      "/members/S001150/alignment",
      "/members/S001150/positions?congress=118",
      "/members/S001150/positions?congress=119",
    ]);
    expect(screen.getByRole("link", { name: "All, 2 votes" }).getAttribute("aria-current")).toBe("true");
  });

  it("shows the switch under Your representatives, before the cards", async () => {
    withBackend(await backendWith({ "hr-118-5": "yea", "s-119-7": "nay" }), <LocalScorecard offered={offered} />);

    const section = screen.getByRole("region", { name: /Your representatives/ });
    const nav = within(section).getByRole("navigation", { name: "Compare your votes on bills from" });
    expect(within(nav).getAllByRole("link").map((a) => a.getAttribute("aria-label"))).toEqual([
      "All, 2 votes",
      "2025–26 (119th), 1 vote",
      "2023–24 (118th), 1 vote",
    ]);
    const heading = within(section).getByRole("heading", { level: 2 });
    const cards = within(section).getAllByRole("list")[0];
    expect(heading.compareDocumentPosition(nav) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(nav.compareDocumentPosition(cards) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(await screen.findByText("Bills compared")).toBeTruthy();
  });

  it("has no switch for votes in one congress, though roll calls are loaded for two", async () => {
    withBackend(await backendWith({ "s-119-7": "nay" }), <LocalScorecard offered={offered} />);

    expect(await screen.findByText("Bills compared")).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "Compare your votes on bills from" })).toBeNull();
  });

  it("shows the switch, with the new count, once a vote in a second congress comes in", async () => {
    const backend = await backendWith({ "s-119-7": "nay" });
    withBackend(backend, <LocalScorecard offered={offered} />);
    expect(screen.queryByRole("navigation")).toBeNull();

    await act(() => backend.setVote("hr-118-5", "yea", "Title of hr-118-5"));
    expect(screen.getByRole("link", { name: "2023–24 (118th), 1 vote" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "All, 2 votes" })).toBeTruthy();
    expect(await screen.findAllByText("Bills compared")).toBeTruthy();
  });

  it("compares every congress for ?congress=all from a /vote or /bills link", async () => {
    search = "congress=all";
    withBackend(await backendWith({ "hr-118-5": "yea", "s-119-7": "nay" }), <LocalScorecard offered={offered} />);

    expect(await screen.findByText("Bills compared")).toBeTruthy();
    expect(urls.filter((u) => u.includes("/positions")).sort()).toEqual([
      "/members/S001150/positions?congress=118",
      "/members/S001150/positions?congress=119",
    ]);
    expect(screen.getByRole("link", { name: "All, 2 votes" }).getAttribute("aria-current")).toBe("true");
  });

  it("compares only the congress in the URL, labeling rows with the chamber the member voted in", async () => {
    search = "congress=118";
    withBackend(await backendWith({ "hr-118-5": "yea", "s-119-7": "nay" }), <LocalScorecard offered={offered} />);

    expect(await screen.findByText("Bills compared")).toBeTruthy();
    expect(urls.sort()).toEqual([
      "/members/S001150",
      "/members/S001150/alignment",
      "/members/S001150/positions?congress=118",
    ]);
    expect(screen.getByText("118th Congress · House")).toBeTruthy();
    expect(screen.queryByText("Title of s-119-7")).toBeNull();
  });

  it("says when the visitor hasn't voted on any bill of the chosen congress", async () => {
    search = "congress=118";
    withBackend(await backendWith({ "s-119-7": "nay" }), <LocalScorecard offered={offered} />);

    expect(screen.getByText("No votes from the 118th Congress yet")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Start voting" }).getAttribute("href")).toBe("/vote?congress=118");
    // The switch is hidden (one congress voted in), so the message is the way back to every vote.
    expect(screen.queryByRole("navigation")).toBeNull();
    expect(screen.getByRole("link", { name: "Compare all your votes" }).getAttribute("href")).toBe("/scorecard");
    // Only the card's profile (party and photo); nothing to compare, so no positions.
    await waitFor(() => expect(urls).toEqual(["/members/S001150"]));
  });
});
