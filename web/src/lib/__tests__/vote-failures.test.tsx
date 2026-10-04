// @vitest-environment jsdom
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { ApiError } from "../api";
import { billList } from "../examples";
import type { LocalEnv } from "../local/storage";
import { createVoteStore } from "../local/votes";
import type { UserVoteChoice } from "../types";
import { isVoteCapReached, localVoteBackend, VOTE_CAP_REACHED, type VoteBackend } from "../votes/backend";
import type { DeckCard } from "../vote-deck";
import { VoteBackendContext } from "../votes/hooks";
import { VotingSession } from "@/app/(app)/vote/voting-session";
import { VoteSection } from "@/app/(app)/bills/[id]/vote-section";
import { deckOf } from "@/test/deck";

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

/** The API's answer to a vote past the account's daily cap (api/internal/handler/user.go CastVote). */
function capError(): ApiError {
  const body = JSON.stringify({ error: "you can vote on at most 500 bills a day; try again later", code: "daily_vote_cap" });
  return new ApiError(429, "Too Many Requests", body);
}

/**
 * A browser-backed vote backend whose next `failures` saves reject, like an account backend offline,
 * with `error` (a plain Error by default).
 */
function flakyBackend(failures: number, seed?: [string, UserVoteChoice], error: () => Error = () => new Error("offline")) {
  const store = createVoteStore({ env: fakeEnv, persist: () => undefined });
  if (seed) store.setVote(seed[0], seed[1]);
  const local = localVoteBackend(store);
  let left = failures;
  const setVote = vi.fn(async (billId: string, vote: UserVoteChoice, title?: string) => {
    if (left > 0) {
      left--;
      throw error();
    }
    await local.setVote(billId, vote, title);
  });
  const backend: VoteBackend = { ...local, getServerSnapshot: local.getSnapshot, setVote };
  return { backend, setVote, store };
}

function withBackend(backend: VoteBackend, child: ReactNode) {
  return render(<VoteBackendContext.Provider value={backend}>{child}</VoteBackendContext.Provider>);
}

function pressed(name: string): string | null {
  return screen.getByRole("button", { name }).getAttribute("aria-pressed");
}

describe("bill page vote after a failed save", () => {
  it("keeps showing the stored vote, says which vote failed, and resends it on Try again", async () => {
    const { backend, setVote, store } = flakyBackend(1, ["hr-119-1", "nay"]);
    withBackend(backend, <VoteSection billId="hr-119-1" billTitle="Example Act" />);
    expect(pressed("Nay")).toBe("true");

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Yea" })));

    expect(screen.getByRole("alert").textContent).toContain("Your Yea vote couldn't be saved.");
    expect(pressed("Yea")).toBe("false");
    expect(pressed("Nay")).toBe("true");
    expect(store.getSnapshot().value["hr-119-1"].vote).toBe("nay");

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Try again" })));

    expect(setVote).toHaveBeenCalledTimes(2);
    expect(setVote).toHaveBeenLastCalledWith("hr-119-1", "yea", "Example Act");
    expect(screen.queryByRole("alert")).toBeNull();
    expect(pressed("Yea")).toBe("true");
    expect(store.getSnapshot().value["hr-119-1"].vote).toBe("yea");
  });

  it("shows no error or retry when the save works", async () => {
    const { backend } = flakyBackend(0);
    withBackend(backend, <VoteSection billId="hr-119-1" billTitle="Example Act" />);
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Skip" })));
    expect(screen.queryByRole("alert")).toBeNull();
    expect(pressed("Skip")).toBe("true");
  });
});

describe("isVoteCapReached", () => {
  it("is true only for a 429 with the daily_vote_cap code", () => {
    expect(isVoteCapReached(capError())).toBe(true);
    const limited = JSON.stringify({ error: "rate limit exceeded", code: "rate_limited" });
    expect(isVoteCapReached(new ApiError(429, "Too Many Requests", limited))).toBe(false);
    expect(isVoteCapReached(new ApiError(429, "Too Many Requests", "not json"))).toBe(false);
    expect(isVoteCapReached(new ApiError(500, "Internal Server Error", '{"code":"daily_vote_cap"}'))).toBe(false);
    expect(isVoteCapReached(new Error("daily_vote_cap"))).toBe(false);
  });
});

describe("bill page vote past the daily vote cap", () => {
  it("says the limit is reached, offers no Try again, and doesn't show the vote as saved", async () => {
    const { backend, store } = flakyBackend(1, ["hr-119-1", "nay"], capError);
    withBackend(backend, <VoteSection billId="hr-119-1" billTitle="Example Act" />);

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Yea" })));

    expect(screen.getByRole("alert").textContent).toBe(VOTE_CAP_REACHED);
    expect(screen.queryByText("Your Yea vote couldn't be saved.")).toBeNull();
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
    expect(pressed("Yea")).toBe("false");
    expect(pressed("Nay")).toBe("true");
    expect(store.getSnapshot().value["hr-119-1"].vote).toBe("nay");
  });

  it("still shows the failure and Try again for any other 429", async () => {
    const limited = () => new ApiError(429, "Too Many Requests", '{"error":"rate limit exceeded","code":"rate_limited"}');
    const { backend } = flakyBackend(1, undefined, limited);
    withBackend(backend, <VoteSection billId="hr-119-1" billTitle="Example Act" />);

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Yea" })));

    expect(screen.getByRole("alert").textContent).toContain("Your Yea vote couldn't be saved.");
    expect(screen.queryByText(VOTE_CAP_REACHED)).toBeNull();
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
  });
});

describe("/vote after a failed save", () => {
  const cards: DeckCard[] = billList.items.slice(0, 3).map((bill) => ({ bill, summary: null }));

  function cardTitle(): string {
    return screen.getByRole("heading", { level: 2 }).textContent ?? "";
  }

  function card(): HTMLElement {
    const el = screen.getByRole("heading", { level: 2 }).closest<HTMLElement>(".cursor-grab");
    if (!el) throw new Error("no swipe card");
    return el;
  }

  it("stays on the card, doesn't count the vote, and moves on once Try again saves it", async () => {
    vi.useFakeTimers();
    const { backend, setVote } = flakyBackend(1);
    withBackend(backend, <VotingSession {...deckOf(cards)} />);
    const first = cardTitle();
    const billId = cards.find((c) => c.bill.title === first)?.bill.id;

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Yea" })));
    await act(async () => vi.advanceTimersByTime(1000));

    expect(screen.getByRole("alert").textContent).toContain("Your Yea vote couldn't be saved.");
    expect(cardTitle()).toBe(first);
    expect(screen.getByText("Laws: 3 of 3 bills left")).toBeTruthy();
    expect(screen.getByText("0 voted this visit")).toBeTruthy();
    // The click animated the card out; after the failure it's back in place, not left invisible.
    expect(card().style.opacity).not.toBe("0");
    expect(card().style.transform).toBe("");

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Try again" })));
    await act(async () => vi.advanceTimersByTime(1000));

    expect(setVote).toHaveBeenCalledTimes(2);
    expect(setVote).toHaveBeenLastCalledWith(billId, "yea", first);
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByText("Laws: 2 of 3 bills left")).toBeTruthy();
    expect(screen.getByText("1 voted this visit")).toBeTruthy();
    expect(cardTitle()).not.toBe(first);
  });

  it("clears the error when the visitor votes another way instead", async () => {
    vi.useFakeTimers();
    const { backend, setVote } = flakyBackend(1);
    withBackend(backend, <VotingSession {...deckOf(cards)} />);

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Nay" })));
    expect(screen.getByRole("alert").textContent).toContain("Your Nay vote couldn't be saved.");

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Skip" })));
    await act(async () => vi.advanceTimersByTime(1000));

    expect(setVote).toHaveBeenLastCalledWith(expect.any(String), "skip", expect.any(String));
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByText("Laws: 2 of 3 bills left")).toBeTruthy();
    expect(screen.getByText("0 voted this visit")).toBeTruthy();
  });
});

describe("/vote past the daily vote cap", () => {
  const cards: DeckCard[] = billList.items.slice(0, 3).map((bill) => ({ bill, summary: null }));

  it("stays on the card, says the limit is reached, and offers no Try again", async () => {
    vi.useFakeTimers();
    const { backend, setVote } = flakyBackend(1, undefined, capError);
    withBackend(backend, <VotingSession {...deckOf(cards)} />);
    const first = screen.getByRole("heading", { level: 2 }).textContent;

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Yea" })));
    await act(async () => vi.advanceTimersByTime(1000));

    expect(setVote).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("alert").textContent).toBe(VOTE_CAP_REACHED);
    expect(screen.queryByText("Your Yea vote couldn't be saved.")).toBeNull();
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe(first);
    expect(screen.getByText("Laws: 3 of 3 bills left")).toBeTruthy();
    expect(screen.getByText("0 voted this visit")).toBeTruthy();
  });
});
