// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { billList } from "@/lib/examples";
import type { LocalEnv } from "@/lib/local/storage";
import { createVoteStore } from "@/lib/local/votes";
import type { UserVoteChoice } from "@/lib/types";
import type { DeckCard } from "@/lib/vote-deck";
import { localVoteBackend, type VoteBackend } from "@/lib/votes/backend";
import { VoteBackendContext } from "@/lib/votes/hooks";
import { getAlert } from "@/test/alert";
import { axeViolations } from "@/test/axe";
import { deckOf } from "@/test/deck";
import { VotingSession } from "../voting-session";

// #967: after a vote, focus goes to what the deck shows next, so a keyboard or screen reader user
// doesn't fall back to the page body and tab through the header and filters again.

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const [first, second] = billList.items;
const cards: DeckCard[] = [first, second].map((bill) => ({ bill, summary: null }));

/** A device vote backend whose next `failures` saves reject, as an account backend offline does. */
function backendFailing(failures: number): VoteBackend {
  const data = new Map<string, string>();
  const env = (): LocalEnv => ({
    storage: {
      getItem: (k) => data.get(k) ?? null,
      setItem: (k, v) => void data.set(k, v),
      removeItem: (k) => void data.delete(k),
    },
    events: new EventTarget(),
  });
  const local = localVoteBackend(createVoteStore({ env, persist: () => undefined }));
  let left = failures;
  return {
    ...local,
    getServerSnapshot: local.getSnapshot,
    setVote: async (billId: string, vote: UserVoteChoice, title?: string) => {
      if (left-- > 0) throw new Error("offline");
      await local.setVote(billId, vote, title);
    },
  };
}

function renderDeck(deck: DeckCard[], failures = 0) {
  return render(
    <VoteBackendContext.Provider value={backendFailing(failures)}>
      <VotingSession {...deckOf(deck)} />
    </VoteBackendContext.Provider>
  );
}

/** Presses a vote button the way a keyboard does: it has focus, then it's activated. */
async function pressWithKeyboard(name: string) {
  const button = screen.getByRole("button", { name });
  button.focus();
  expect(document.activeElement).toBe(button);
  await act(async () => fireEvent.click(button));
  // The card's exit animation, before the next one is dealt.
  await act(async () => vi.advanceTimersByTime(1000));
}

describe("/vote keyboard focus", () => {
  it("leaves focus alone on page load, so the skip link and page heading come first", () => {
    renderDeck(cards);
    expect(screen.getByRole("heading", { level: 2, name: first.title })).toBeTruthy();
    expect(document.activeElement).toBe(document.body);
  });

  it("moves focus to the next card's title after a vote is saved", async () => {
    vi.useFakeTimers();
    const { container } = renderDeck(cards);

    await pressWithKeyboard("Yea");

    const title = screen.getByRole("heading", { level: 2, name: second.title });
    expect(document.activeElement).toBe(title);
    vi.useRealTimers();
    expect(await axeViolations(container)).toEqual([]);
  });

  it("returns focus to the same card's title when the save fails, and the error is in the alert", async () => {
    vi.useFakeTimers();
    renderDeck(cards, 1);

    await pressWithKeyboard("Nay");

    expect(document.activeElement).toBe(screen.getByRole("heading", { level: 2, name: first.title }));
    expect(getAlert().textContent).toContain("Your Nay vote couldn't be saved.");

    // Try again saves it this time, and focus moves on to the next card.
    await pressWithKeyboard("Try again");
    expect(document.activeElement).toBe(screen.getByRole("heading", { level: 2, name: second.title }));
  });

  it("moves focus to the end-of-deck message after the last vote", async () => {
    vi.useFakeTimers();
    const { container } = renderDeck(cards.slice(0, 1));

    await pressWithKeyboard("Skip");

    expect(document.activeElement).toBe(screen.getByRole("heading", { name: "You've voted on every bill here" }));
    vi.useRealTimers();
    expect(await axeViolations(container)).toEqual([]);
  });
});
