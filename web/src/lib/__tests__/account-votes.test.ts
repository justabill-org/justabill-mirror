import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createAccountVoteBackend,
  loadAccountVotes,
  MAX_VOTES_OFFSET,
  VOTES_PAGE_SIZE,
  type AccountVotesApi,
} from "../votes/account";
import { fixedVoteBackend } from "../votes/backend";
import type { UserVote } from "../types";

// Signed-in votes (#138): they live in the account, read with GET /me/votes and written with
// POST /bills/{id}/vote. The API is faked.

function row(billId: string, vote: UserVote["vote"], at = "2026-10-01T12:00:00.000Z"): UserVote {
  return { user_id: "u-1", bill_id: billId, vote, voted_at: at };
}

function fakeApi(rows: UserVote[] = []) {
  return {
    getMyVotes: vi.fn<AccountVotesApi["getMyVotes"]>(async (_token, { offset = 0, limit = 20 }) => ({
      items: rows.slice(offset, offset + limit),
      total: rows.length,
      offset,
      limit,
    })),
    castVote: vi.fn<AccountVotesApi["castVote"]>(async () => ({ status: "ok" })),
    deleteVote: vi.fn<AccountVotesApi["deleteVote"]>(async () => undefined),
  };
}

const getIdToken = vi.fn(async () => "id-token-1");
const NOW = new Date("2026-10-05T09:00:00.000Z");
const flush = () => new Promise((r) => setTimeout(r, 0));

function subscribed(api: ReturnType<typeof fakeApi>, titles = {}) {
  const backend = createAccountVoteBackend({ getIdToken, api, titles: () => titles, now: () => NOW });
  const listener = vi.fn();
  const stop = backend.subscribe(listener);
  return { backend, listener, stop };
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("loadAccountVotes", () => {
  it("reads every page of GET /me/votes with the token", async () => {
    const rows = Array.from({ length: VOTES_PAGE_SIZE + 5 }, (_, i) => row(`hr-119-${i + 1}`, "yea"));
    const api = fakeApi(rows);
    expect(await loadAccountVotes(api, "tok")).toHaveLength(rows.length);
    expect(api.getMyVotes.mock.calls).toEqual([
      ["tok", { offset: 0, limit: VOTES_PAGE_SIZE }],
      ["tok", { offset: VOTES_PAGE_SIZE, limit: VOTES_PAGE_SIZE }],
    ]);
  });

  it("stops at the total even when the last page is full, and at the API's deepest offset", async () => {
    const full = fakeApi(Array.from({ length: VOTES_PAGE_SIZE }, (_, i) => row(`s-119-${i + 1}`, "nay")));
    await loadAccountVotes(full, "tok");
    expect(full.getMyVotes).toHaveBeenCalledTimes(1);

    const endless: AccountVotesApi = {
      getMyVotes: vi.fn(async (_t, { offset = 0 }) => ({
        items: Array.from({ length: VOTES_PAGE_SIZE }, (_, i) => row(`hr-119-${offset + i}`, "yea")),
        total: 1_000_000,
        offset,
        limit: VOTES_PAGE_SIZE,
      })),
      castVote: vi.fn(),
      deleteVote: vi.fn(),
    };
    await loadAccountVotes(endless, "tok");
    expect(endless.getMyVotes).toHaveBeenCalledTimes(MAX_VOTES_OFFSET / VOTES_PAGE_SIZE + 1);
  });
});

describe("createAccountVoteBackend", () => {
  it("fetches nothing until something subscribes, and renders as loading on the server", () => {
    const api = fakeApi();
    const backend = createAccountVoteBackend({ getIdToken, api });
    expect(api.getMyVotes).not.toHaveBeenCalled();
    expect(backend.getSnapshot().storage).toBe("server");
    expect(backend.getServerSnapshot()).toEqual({ votes: {}, storage: "server" });
  });

  it("loads the account's votes, with titles from this device where it has them", async () => {
    const api = fakeApi([row("hr-119-1", "yea"), row("s-119-2", "skip")]);
    const { backend, listener } = subscribed(api, { "hr-119-1": { vote: "nay", at: "x", title: "A bill" } });
    await flush();
    expect(listener).toHaveBeenCalled();
    expect(backend.getSnapshot()).toEqual({
      storage: "account",
      votes: {
        "hr-119-1": { vote: "yea", at: "2026-10-01T12:00:00.000Z", title: "A bill" },
        "s-119-2": { vote: "skip", at: "2026-10-01T12:00:00.000Z" },
      },
    });
  });

  it("keeps the API's title over this device's, and falls back where the API sends none", async () => {
    const api = fakeApi([
      { ...row("hr-119-1", "yea"), title: "Lower Costs Act" },
      row("s-119-2", "nay"), // an older API, or a bill that's gone
      { ...row("hr-119-3", "skip"), title: "Cast on another device" },
    ]);
    const { backend } = subscribed(api, {
      "hr-119-1": { vote: "yea", at: "x", title: "An older title" },
      "s-119-2": { vote: "nay", at: "x", title: "From this device" },
    });
    await flush();
    const votes = backend.getSnapshot().votes;
    expect(votes["hr-119-1"]?.title).toBe("Lower Costs Act");
    expect(votes["s-119-2"]?.title).toBe("From this device");
    expect(votes["hr-119-3"]).toEqual({ vote: "skip", at: "2026-10-01T12:00:00.000Z", title: "Cast on another device" });
  });

  it("keeps snapshots stable between changes", async () => {
    const { backend } = subscribed(fakeApi([row("hr-119-1", "yea")]));
    await flush();
    expect(backend.getSnapshot()).toBe(backend.getSnapshot());
  });

  it("casts a vote with POST /bills/{id}/vote, then shows it", async () => {
    const api = fakeApi();
    const { backend } = subscribed(api);
    await flush();
    await backend.setVote("hr-119-7", "nay", "Seven");
    expect(api.castVote).toHaveBeenCalledWith("id-token-1", "hr-119-7", "nay");
    expect(backend.getSnapshot().votes["hr-119-7"]).toEqual({ vote: "nay", at: NOW.toISOString(), title: "Seven" });
  });

  it("keeps the old vote and rejects when the API refuses the new one", async () => {
    const api = fakeApi([row("hr-119-1", "yea")]);
    const { backend } = subscribed(api);
    await flush();
    api.castVote.mockRejectedValueOnce(new Error("429"));
    await expect(backend.setVote("hr-119-1", "nay")).rejects.toThrow("429");
    expect(backend.getSnapshot().votes["hr-119-1"].vote).toBe("yea");
  });

  it("keeps a vote cast while the list loads over the loaded one", async () => {
    let release!: () => void;
    const api = fakeApi([row("hr-119-1", "yea")]);
    const load = api.getMyVotes.getMockImplementation()!;
    api.getMyVotes.mockImplementationOnce(async (t, p) => {
      await new Promise<void>((r) => (release = r));
      return load(t, p);
    });
    const { backend } = subscribed(api);
    await flush();
    await backend.setVote("hr-119-1", "nay", "One");
    expect(backend.getSnapshot().storage).toBe("server");
    release();
    await flush();
    expect(backend.getSnapshot()).toMatchObject({ storage: "account", votes: { "hr-119-1": { vote: "nay", title: "One" } } });
  });

  it("removes a vote with DELETE /bills/{id}/vote, then drops it (#593)", async () => {
    const api = fakeApi([row("hr-119-1", "yea"), row("s-119-2", "nay")]);
    const { backend, listener } = subscribed(api);
    await flush();
    listener.mockClear();
    await backend.clearVote("hr-119-1");
    expect(api.deleteVote).toHaveBeenCalledWith("id-token-1", "hr-119-1");
    expect(listener).toHaveBeenCalled();
    expect(backend.getSnapshot()).toEqual({
      storage: "account",
      votes: { "s-119-2": { vote: "nay", at: "2026-10-01T12:00:00.000Z" } },
    });
  });

  it("keeps the vote and rejects when the API refuses to remove it", async () => {
    const api = fakeApi([row("hr-119-1", "yea")]);
    const { backend } = subscribed(api);
    await flush();
    api.deleteVote.mockRejectedValueOnce(new Error("503"));
    await expect(backend.clearVote("hr-119-1")).rejects.toThrow("503");
    expect(backend.getSnapshot().votes["hr-119-1"].vote).toBe("yea");
  });

  it("doesn't bring back a removed vote from a load that started before it, or a cast one later", async () => {
    let release!: () => void;
    const api = fakeApi([row("hr-119-1", "yea")]);
    const load = api.getMyVotes.getMockImplementation()!;
    api.getMyVotes.mockImplementationOnce(async (t, p) => {
      await new Promise<void>((r) => (release = r));
      return load(t, p);
    });
    const { backend } = subscribed(api);
    await flush();
    await backend.clearVote("hr-119-1");
    release();
    await flush();
    expect(backend.getSnapshot()).toEqual({ storage: "account", votes: {} });

    // Cast, then removed in this session: a refresh (the API no longer has it) keeps it gone.
    await backend.setVote("hr-119-2", "nay", "Two");
    await backend.clearVote("hr-119-2");
    backend.refresh?.();
    await flush();
    expect(backend.getSnapshot().votes["hr-119-2"]).toBeUndefined();

    // Voting again after removing it shows the new vote.
    await backend.setVote("hr-119-1", "skip");
    expect(backend.getSnapshot().votes["hr-119-1"]).toEqual({ vote: "skip", at: NOW.toISOString() });
  });

  it("says the votes are unavailable when they can't load, refuses votes, and recovers on refresh", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const api = fakeApi([row("hr-119-1", "yea")]);
    api.getMyVotes.mockRejectedValueOnce(new Error("503"));
    const { backend } = subscribed(api);
    await flush();
    expect(backend.getSnapshot()).toEqual({ votes: {}, storage: "unavailable" });
    await expect(backend.setVote("hr-119-2", "yea")).rejects.toThrow();
    expect(api.castVote).not.toHaveBeenCalled();
    await expect(backend.clearVote("hr-119-1")).rejects.toThrow();
    expect(api.deleteVote).not.toHaveBeenCalled();

    backend.refresh?.();
    expect(backend.getSnapshot().storage).toBe("server");
    await flush();
    expect(backend.getSnapshot().storage).toBe("account");
    expect(backend.getSnapshot().votes["hr-119-1"].vote).toBe("yea");
  });

  it("loads again after a refresh that comes in during a load (e.g. an import)", async () => {
    const api = fakeApi([row("hr-119-1", "yea")]);
    const { backend } = subscribed(api);
    backend.refresh?.();
    await flush();
    await flush();
    expect(api.getMyVotes).toHaveBeenCalledTimes(2);
  });

  it("loads once however many components subscribe", async () => {
    const api = fakeApi();
    const { backend } = subscribed(api);
    backend.subscribe(() => {});
    await flush();
    backend.subscribe(() => {});
    expect(api.getMyVotes).toHaveBeenCalledTimes(1);
  });
});

describe("fixedVoteBackend", () => {
  it("shows nothing and refuses votes while sign-in is worked out or the account failed", async () => {
    const pending = fixedVoteBackend("server");
    expect(pending.getSnapshot()).toEqual({ votes: {}, storage: "server" });
    await expect(pending.setVote("hr-119-1", "yea")).rejects.toThrow(/loading/);
    await expect(pending.clearVote("hr-119-1")).rejects.toThrow(/loading/);
    expect(pending.refresh).toBeUndefined();

    const retry = vi.fn();
    const failed = fixedVoteBackend("unavailable", retry);
    expect(failed.getServerSnapshot().storage).toBe("unavailable");
    await expect(failed.setVote("hr-119-1", "yea")).rejects.toThrow(/unavailable/);
    await expect(failed.clearVote("hr-119-1")).rejects.toThrow(/unavailable/);
    failed.refresh?.();
    expect(retry).toHaveBeenCalled();
  });
});
