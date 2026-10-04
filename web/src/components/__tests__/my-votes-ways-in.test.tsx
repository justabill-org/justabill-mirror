// @vitest-environment jsdom
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { VoteSection } from "@/app/(app)/bills/[id]/vote-section";
import { YourData } from "@/app/(app)/settings/your-data";
import { VotingSession } from "@/app/(app)/vote/voting-session";
import { deckOf } from "@/test/deck";
import { ScorecardView } from "@/components/scorecard/local-scorecard";
import { billList } from "@/lib/examples";
import type { LocalEnv } from "@/lib/local/storage";
import { createVoteStore, type LocalVotes } from "@/lib/local/votes";
import type { DeckCard } from "@/lib/vote-deck";
import { localVoteBackend, type VoteBackend } from "@/lib/votes/backend";
import { VoteBackendContext } from "@/lib/votes/hooks";
import { fakeAuthStore } from "@/test/fake-auth";

// The ways in to My votes (#739): the bill page's vote card, the end of a /vote deck, the
// scorecard's line that replaced its device tools, and Settings' "Your data" card.

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
}));

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

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

/** A device backend over a fresh store, with the server snapshot set to the client's. */
function deviceBackend() {
  const store = createVoteStore({ env: fakeEnv, persist: () => undefined });
  const local = localVoteBackend(store);
  return { store, backend: { ...local, getServerSnapshot: local.getSnapshot } satisfies VoteBackend };
}

function withBackend(backend: VoteBackend, child: ReactNode) {
  return render(<VoteBackendContext.Provider value={backend}>{child}</VoteBackendContext.Provider>);
}

const myVotesLink = (name = "My votes") => screen.queryByRole("link", { name });

describe("the bill page's vote card", () => {
  it("links to all my votes once there's a vote, and not before or after it's removed", async () => {
    const { backend } = deviceBackend();
    withBackend(backend, <VoteSection billId="hr-119-1" billTitle="Example Act" />);
    expect(myVotesLink("See all my votes")).toBeNull();

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Nay" })));
    expect(myVotesLink("See all my votes")?.getAttribute("href")).toBe("/my-votes");

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Remove my vote" })));
    expect(myVotesLink("See all my votes")).toBeNull();
  });
});

describe("the end of a /vote deck", () => {
  const cards: DeckCard[] = billList.items.slice(0, 1).map((bill) => ({ bill, summary: null }));

  it("offers My votes beside Scorecard once every bill in the deck is voted on", async () => {
    vi.useFakeTimers();
    const { backend } = deviceBackend();
    withBackend(backend, <VotingSession {...deckOf(cards)} />);
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Yea" })));
    await act(async () => vi.advanceTimersByTime(300));

    expect(screen.getByRole("heading", { name: "You've voted on every bill here" })).toBeTruthy();
    expect(myVotesLink()?.getAttribute("href")).toBe("/my-votes");
    expect(screen.getByRole("link", { name: "Scorecard" }).getAttribute("href")).toBe("/scorecard");
  });

  it("offers My votes when every bill here was already voted on", () => {
    const { store, backend } = deviceBackend();
    store.setVote(cards[0].bill.id, "skip");
    withBackend(backend, <VotingSession {...deckOf(cards)} />);

    expect(screen.getByRole("heading", { name: "You've voted on every bill here" })).toBeTruthy();
    expect(myVotesLink()?.getAttribute("href")).toBe("/my-votes");
    expect(screen.getByRole("link", { name: "Scorecard" }).getAttribute("href")).toBe("/scorecard");
  });
});

describe("the scorecard's My votes line", () => {
  const votes: LocalVotes = {
    "hr-119-1": { vote: "yea", at: "2026-10-01T00:00:00Z" },
    "hr-119-2": { vote: "skip", at: "2026-10-02T00:00:00Z" },
  };

  it.each([
    ["device", "1 vote so far, kept on this device only."],
    ["memory", "1 vote so far. Votes won't be saved on this browser."],
    ["account", "1 vote so far, saved in your account."],
  ] as const)("says where %s votes are kept, links to My votes, and has no device tools", (storage, where) => {
    render(<ScorecardView votes={votes} storage={storage} reps={null} scores={{ status: "none" }} />);
    const link = myVotesLink();
    expect(link?.getAttribute("href")).toBe("/my-votes");
    expect(link?.closest("p")?.textContent).toContain(where);
    for (const tool of ["Download my votes", "Import votes", "Clear my votes"]) {
      expect(screen.queryByRole("button", { name: tool })).toBeNull();
    }
  });
});

describe("Settings' Your data card", () => {
  it("names My votes as where to change votes", () => {
    const api = { exportMe: vi.fn(), deleteMe: vi.fn() };
    render(<YourData user={fakeAuthStore("signed-in").store} api={api} save={vi.fn()} />);
    expect(myVotesLink()?.getAttribute("href")).toBe("/my-votes");
  });
});
