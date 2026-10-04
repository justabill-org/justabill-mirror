// @vitest-environment jsdom
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { axeViolations } from "@/test/axe";
import type { LocalEnv } from "../local/storage";
import { createVoteStore } from "../local/votes";
import type { UserVote } from "../types";
import { createAccountVoteBackend, type AccountVotesApi } from "../votes/account";
import { localVoteBackend, type VoteBackend } from "../votes/backend";
import { useVotes, VoteBackendContext } from "../votes/hooks";
import { VoteSection } from "@/app/(app)/bills/[id]/vote-section";

// "Remove my vote" on the bill page (#593): signed in it calls DELETE /bills/{id}/vote, signed out
// it clears the vote from this browser and sends nothing.

const fetchSpy = vi.fn();

beforeEach(() => {
  vi.stubGlobal("fetch", fetchSpy);
});

afterEach(() => {
  cleanup();
  fetchSpy.mockReset();
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

function localBackend() {
  const store = createVoteStore({ env: fakeEnv, persist: () => undefined });
  store.setVote("hr-119-1", "yea", "Example Act");
  const local = localVoteBackend(store);
  return { store, backend: { ...local, getServerSnapshot: local.getSnapshot } satisfies VoteBackend };
}

function accountBackend(rows: UserVote[]) {
  const api = {
    getMyVotes: vi.fn<AccountVotesApi["getMyVotes"]>(async () => ({ items: rows, total: rows.length, offset: 0, limit: 100 })),
    castVote: vi.fn<AccountVotesApi["castVote"]>(async () => ({ status: "ok" })),
    deleteVote: vi.fn<AccountVotesApi["deleteVote"]>(async () => undefined),
  };
  const backend = createAccountVoteBackend({ getIdToken: async () => "id-token-1", api });
  return { api, backend: { ...backend, getServerSnapshot: backend.getSnapshot } satisfies VoteBackend };
}

/** Lists the bills useVotes() has, as the scorecard would see them. */
function VotedBills() {
  return <p data-testid="voted">{Object.keys(useVotes().votes).join(",")}</p>;
}

function withBackend(backend: VoteBackend, child: ReactNode) {
  return render(
    <VoteBackendContext.Provider value={backend}>
      {child}
      <VotedBills />
    </VoteBackendContext.Provider>
  );
}

const section = <VoteSection billId="hr-119-1" billTitle="Example Act" />;
const remove = () => screen.getByRole("button", { name: "Remove my vote" });
const pressed = (name: string) => screen.getByRole("button", { name }).getAttribute("aria-pressed");

describe("Remove my vote, signed out", () => {
  it("clears the vote from this browser, says so, and sends nothing", async () => {
    const { store, backend } = localBackend();
    withBackend(backend, section);
    expect(pressed("Yea")).toBe("true");

    await act(async () => fireEvent.click(remove()));

    expect(store.getSnapshot().value).toEqual({});
    expect(pressed("Yea")).toBe("false");
    expect(screen.getByTestId("voted").textContent).toBe("");
    expect(screen.queryByRole("button", { name: "Remove my vote" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Share my vote" })).toBeNull();
    // The button went away with the vote, so focus moves to the note that says it's gone.
    const note = screen.getByRole("status");
    expect(note.textContent).toBe("Your vote was removed.");
    expect(document.activeElement).toBe(note);
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("drops the note when the visitor votes again", async () => {
    const { backend } = localBackend();
    withBackend(backend, section);
    await act(async () => fireEvent.click(remove()));
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Nay" })));
    expect(pressed("Nay")).toBe("true");
    expect(screen.queryByText("Your vote was removed.")).toBeNull();
    expect(remove()).toBeTruthy();
  });
});

describe("Remove my vote, signed in", () => {
  const voted: UserVote[] = [
    { user_id: "u-1", bill_id: "hr-119-1", vote: "nay", voted_at: "2026-10-01T12:00:00.000Z" },
    { user_id: "u-1", bill_id: "s-119-2", vote: "yea", voted_at: "2026-10-01T12:00:00.000Z" },
  ];

  it("calls DELETE through the account and the vote leaves useVotes() without a reload", async () => {
    const { api, backend } = accountBackend(voted);
    withBackend(backend, section);
    expect(await screen.findByRole("button", { name: "Remove my vote" })).toBeTruthy();
    expect(screen.getByTestId("voted").textContent).toBe("hr-119-1,s-119-2");

    await act(async () => fireEvent.click(remove()));

    expect(api.deleteVote).toHaveBeenCalledWith("id-token-1", "hr-119-1");
    expect(api.getMyVotes).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("voted").textContent).toBe("s-119-2");
    expect(pressed("Nay")).toBe("false");
    expect(screen.getByRole("status").textContent).toBe("Your vote was removed.");
  });

  it("keeps the vote and says so when the delete fails, and Try again removes it", async () => {
    const { api, backend } = accountBackend(voted);
    api.deleteVote.mockRejectedValueOnce(new Error("503"));
    withBackend(backend, section);
    await act(async () => fireEvent.click(await screen.findByRole("button", { name: "Remove my vote" })));

    expect(screen.getByRole("alert").textContent).toContain("Your vote couldn't be removed.");
    expect(pressed("Nay")).toBe("true");
    expect(screen.getByTestId("voted").textContent).toBe("hr-119-1,s-119-2");

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Try again" })));

    expect(api.deleteVote).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("alert")).toBeNull();
    expect(pressed("Nay")).toBe("false");
    expect(api.castVote).not.toHaveBeenCalled();
  });
});

describe("Remove my vote, accessibility", () => {
  it("is a keyboard-reachable button only when there's a vote, and passes axe before and after", async () => {
    const { backend } = localBackend();
    const { container } = withBackend(backend, section);
    const button = remove();
    expect(button.tagName).toBe("BUTTON");
    expect(button.tabIndex).toBe(0);
    expect(await axeViolations(container)).toEqual([]);

    await act(async () => fireEvent.click(button));
    expect(await axeViolations(container)).toEqual([]);
  });

  it("isn't shown before a vote", () => {
    const store = createVoteStore({ env: fakeEnv, persist: () => undefined });
    withBackend(localVoteBackend(store), section);
    expect(screen.queryByRole("button", { name: "Remove my vote" })).toBeNull();
  });
});
