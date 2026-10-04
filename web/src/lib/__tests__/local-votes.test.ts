import { afterEach, describe, expect, it, vi } from "vitest";
import type { LocalEnv, StorageLike } from "../local/storage";
import {
  MAX_TITLE_LENGTH,
  MAX_VOTES,
  VOTES_CORRUPT_KEY,
  VOTES_KEY,
  VoteImportError,
  createVoteStore,
  type LocalVote,
} from "../local/votes";
import { localVoteBackend } from "../votes/backend";
import { importBatches } from "../votes/import";

class FakeStorage implements StorageLike {
  data = new Map<string, string>();
  failWrites = false;
  getItem(key: string) {
    return this.data.get(key) ?? null;
  }
  setItem(key: string, value: string) {
    if (this.failWrites) throw new DOMException("quota", "QuotaExceededError");
    this.data.set(key, value);
  }
  removeItem(key: string) {
    this.data.delete(key);
  }
}

function browser(storage: StorageLike | null = new FakeStorage()) {
  const events = new EventTarget();
  const env: LocalEnv = { storage, events };
  return { env, events, storage };
}

function clock(start = Date.parse("2026-10-04T15:00:00Z")) {
  let t = start;
  return () => new Date((t += 1000));
}

function storeIn(env: LocalEnv, persist = vi.fn(async () => true)) {
  return createVoteStore({ env: () => env, now: clock(), persist });
}

function storageEvent(key: string | null) {
  return Object.assign(new Event("storage"), { key });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("server render", () => {
  it("returns an empty snapshot and ignores writes when there is no window", () => {
    expect(typeof window).toBe("undefined");
    const store = createVoteStore();
    expect(store.getSnapshot().value).toEqual({});
    expect(store.getSnapshot().persistence).toBe("server");
    expect(store.getServerSnapshot()).toBe(store.getSnapshot());
    expect(() => store.setVote("hr-119-1", "yea", "A")).not.toThrow();
    expect(store.getSnapshot().value).toEqual({});
    const unsubscribe = store.subscribe(() => undefined);
    expect(unsubscribe).not.toThrow();
  });
});

describe("round trip", () => {
  it("stores the documented format and reads it back in a new page", () => {
    const { env, storage } = browser();
    const store = storeIn(env);
    store.setVote("hr-119-1", "yea", "Example Act of 2025");

    const stored = JSON.parse((storage as FakeStorage).getItem(VOTES_KEY)!);
    expect(stored).toEqual({
      v: 1,
      votes: { "hr-119-1": { vote: "yea", at: "2026-10-04T15:00:01.000Z", title: "Example Act of 2025" } },
    });

    const reloaded = storeIn(env);
    expect(reloaded.getSnapshot().value["hr-119-1"].vote).toBe("yea");
    expect(reloaded.getSnapshot().persistence).toBe("device");
  });

  it("keeps snapshots stable until something changes", () => {
    const { env } = browser();
    const store = storeIn(env);
    const first = store.getSnapshot();
    expect(store.getSnapshot()).toBe(first);
    store.setVote("hr-119-1", "nay");
    expect(store.getSnapshot()).not.toBe(first);
  });

  it("changes a vote, keeps its title, and clears", () => {
    const { env } = browser();
    const store = storeIn(env);
    store.setVote("hr-119-1", "yea", "Title");
    store.setVote("hr-119-1", "nay");
    expect(store.getSnapshot().value["hr-119-1"]).toMatchObject({ vote: "nay", title: "Title" });
    store.clearVote("hr-119-1");
    expect(store.getSnapshot().value).toEqual({});
    store.setVote("s-119-2", "skip");
    store.setVote("hr-119-3", "yea");
    store.keepOnly(["hr-119-3", "hr-119-404"]);
    expect(Object.keys(store.getSnapshot().value)).toEqual(["hr-119-3"]);
    store.clearAll();
    expect(store.getSnapshot().value).toEqual({});
  });

  it("shortens long titles", () => {
    const { env } = browser();
    const store = storeIn(env);
    store.setVote("hr-119-1", "yea", "x".repeat(1000));
    const title = store.getSnapshot().value["hr-119-1"].title!;
    expect(title).toHaveLength(MAX_TITLE_LENGTH);
    expect(title.endsWith("…")).toBe(true);
  });

  it("sends no request when voting", async () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    const { env } = browser();
    const backend = localVoteBackend(storeIn(env));
    await backend.setVote("hr-119-1", "yea", "Example Act");
    expect(backend.getSnapshot()).toMatchObject({ storage: "device", votes: { "hr-119-1": { vote: "yea" } } });
    expect(backend.getSnapshot()).toBe(backend.getSnapshot());
    expect(fetch).not.toHaveBeenCalled();
  });
});

describe("persistence request", () => {
  it("asks once, after the first vote", () => {
    const { env } = browser();
    const persist = vi.fn(async () => true);
    const store = storeIn(env, persist);
    store.getSnapshot();
    expect(persist).not.toHaveBeenCalled();
    store.setVote("hr-119-1", "yea");
    store.setVote("hr-119-2", "nay");
    expect(persist).toHaveBeenCalledTimes(1);
  });

  it("survives a browser without the Storage API or a rejected request", () => {
    const { env } = browser();
    expect(() => createVoteStore({ env: () => env, persist: () => undefined }).setVote("hr-119-1", "yea")).not.toThrow();
    const rejects = createVoteStore({ env: () => env, persist: () => Promise.reject(new Error("no")) });
    expect(() => rejects.setVote("hr-119-1", "yea")).not.toThrow();
    const throws = createVoteStore({
      env: () => env,
      persist: () => {
        throw new Error("no");
      },
    });
    expect(() => throws.setVote("hr-119-1", "yea")).not.toThrow();
  });
});

describe("cross-tab updates", () => {
  it("picks up another tab's vote through the storage event", () => {
    const shared = new FakeStorage();
    const tabA = browser(shared);
    const tabB = browser(shared);
    const a = storeIn(tabA.env);
    const b = storeIn(tabB.env);
    const listener = vi.fn();
    const unsubscribe = b.subscribe(listener);
    expect(b.getSnapshot().value).toEqual({});

    a.setVote("hr-119-1", "yea");
    tabB.events.dispatchEvent(storageEvent(VOTES_KEY));
    expect(listener).toHaveBeenCalledTimes(1);
    expect(b.getSnapshot().value["hr-119-1"].vote).toBe("yea");

    tabB.events.dispatchEvent(storageEvent("some.other.key"));
    expect(listener).toHaveBeenCalledTimes(1);

    shared.data.clear();
    tabB.events.dispatchEvent(storageEvent(null));
    expect(b.getSnapshot().value).toEqual({});

    unsubscribe();
    a.setVote("hr-119-2", "nay");
    tabB.events.dispatchEvent(storageEvent(VOTES_KEY));
    expect(listener).toHaveBeenCalledTimes(2);
  });
});

describe("cap and eviction", () => {
  function fill(n: number, vote: (i: number) => LocalVote["vote"]) {
    const votes: Record<string, LocalVote> = {};
    for (let i = 0; i < n; i++) {
      votes[`hr-119-${i + 1}`] = { vote: vote(i), at: new Date(Date.UTC(2026, 0, 1) + i * 60_000).toISOString() };
    }
    return votes;
  }

  it("drops the oldest skips first", () => {
    const storage = new FakeStorage();
    // Every tenth entry is a skip; the first skip is the oldest entry overall.
    storage.setItem(VOTES_KEY, JSON.stringify({ v: 1, votes: fill(MAX_VOTES, (i) => (i % 10 === 0 ? "skip" : "yea")) }));
    const store = storeIn(browser(storage).env);
    store.setVote("s-119-1", "nay");
    store.setVote("s-119-2", "nay");

    const votes = store.getSnapshot().value;
    expect(Object.keys(votes)).toHaveLength(MAX_VOTES);
    expect(votes["hr-119-1"]).toBeUndefined(); // oldest skip
    expect(votes["hr-119-11"]).toBeUndefined(); // next-oldest skip
    expect(votes["hr-119-2"]).toBeDefined(); // older than that skip, but a real vote
    expect(votes["hr-119-21"]).toBeDefined();
    expect(votes["s-119-2"].vote).toBe("nay");
  });

  it("drops the oldest votes when there are no skips", () => {
    const storage = new FakeStorage();
    storage.setItem(VOTES_KEY, JSON.stringify({ v: 1, votes: fill(MAX_VOTES, () => "yea") }));
    const store = storeIn(browser(storage).env);
    store.setVote("s-119-1", "nay");
    const votes = store.getSnapshot().value;
    expect(Object.keys(votes)).toHaveLength(MAX_VOTES);
    expect(votes["hr-119-1"]).toBeUndefined();
    expect(votes["s-119-1"]).toBeDefined();
  });
});

describe("corrupt data", () => {
  it.each([
    ["invalid JSON", "{not json"],
    ["an unknown version", JSON.stringify({ v: 2, votes: { "hr-119-1": { vote: "yea", at: "2026-10-04T00:00:00Z" } } })],
    ["the wrong shape", JSON.stringify([1, 2, 3])],
  ])("moves %s aside and starts empty", (_, raw) => {
    const storage = new FakeStorage();
    storage.setItem(VOTES_KEY, raw);
    const store = storeIn(browser(storage).env);
    expect(store.getSnapshot()).toMatchObject({ value: {}, persistence: "device" });
    expect(storage.getItem(VOTES_CORRUPT_KEY)).toBe(raw);
    expect(store.readCorrupt()).toBe(raw);

    store.setVote("hr-119-1", "yea");
    expect(storage.getItem(VOTES_CORRUPT_KEY)).toBe(raw);
  });

  it("keeps the valid entries of a partly bad store and the whole raw value aside", () => {
    const storage = new FakeStorage();
    const raw = JSON.stringify({
      v: 1,
      votes: {
        "hr-119-1": { vote: "yea", at: "2026-10-04T00:00:00Z" },
        "hr-119-2": { vote: "maybe", at: "2026-10-04T00:00:00Z" },
        "not an id": { vote: "nay", at: "2026-10-04T00:00:00Z" },
        "hr-119-3": { vote: "nay", at: "yesterday-ish" },
      },
    });
    storage.setItem(VOTES_KEY, raw);
    const store = storeIn(browser(storage).env);
    expect(Object.keys(store.getSnapshot().value)).toEqual(["hr-119-1"]);
    expect(storage.getItem(VOTES_CORRUPT_KEY)).toBe(raw);
    expect(JSON.parse(storage.getItem(VOTES_KEY)!).votes).toEqual({
      "hr-119-1": { vote: "yea", at: "2026-10-04T00:00:00.000Z" },
    });
  });

  it("doesn't overwrite an earlier corrupt value", () => {
    const storage = new FakeStorage();
    storage.setItem(VOTES_CORRUPT_KEY, "first");
    storage.setItem(VOTES_KEY, "second");
    storeIn(browser(storage).env).getSnapshot();
    expect(storage.getItem(VOTES_CORRUPT_KEY)).toBe("first");
    const others = [...storage.data.keys()].filter((k) => k.startsWith(`${VOTES_CORRUPT_KEY}.`));
    expect(others.map((k) => storage.getItem(k))).toEqual(["second"]);
  });

  it("stays in memory, without touching the data, when it can't be moved aside", () => {
    const storage = new FakeStorage();
    storage.setItem(VOTES_KEY, "{bad");
    storage.failWrites = true;
    const store = storeIn(browser(storage).env);
    store.setVote("hr-119-1", "yea");
    expect(store.getSnapshot()).toMatchObject({ persistence: "memory", value: { "hr-119-1": { vote: "yea" } } });
    expect(storage.getItem(VOTES_KEY)).toBe("{bad");
  });
});

describe("storage unavailable", () => {
  it("works in memory when there is no storage", () => {
    const store = storeIn(browser(null).env);
    store.setVote("hr-119-1", "yea");
    expect(store.getSnapshot()).toMatchObject({ persistence: "memory", value: { "hr-119-1": { vote: "yea" } } });
  });

  it("works in memory when reads throw", () => {
    const storage: StorageLike = {
      getItem: () => {
        throw new DOMException("denied", "SecurityError");
      },
      setItem: () => undefined,
      removeItem: () => undefined,
    };
    const store = storeIn(browser(storage).env);
    store.setVote("hr-119-1", "nay");
    expect(store.getSnapshot()).toMatchObject({ persistence: "memory", value: { "hr-119-1": { vote: "nay" } } });
  });

  it("falls back to memory when a write fails, keeping the vote", () => {
    const storage = new FakeStorage();
    const store = storeIn(browser(storage).env);
    store.setVote("hr-119-1", "yea");
    storage.failWrites = true;
    store.setVote("hr-119-2", "nay");
    expect(store.getSnapshot().persistence).toBe("memory");
    expect(Object.keys(store.getSnapshot().value)).toEqual(["hr-119-1", "hr-119-2"]);
  });

  it("finds no storage when localStorage throws", async () => {
    vi.stubGlobal("window", {
      get localStorage() {
        throw new DOMException("denied", "SecurityError");
      },
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    });
    const { browserEnv } = await import("../local/storage");
    expect(browserEnv()?.storage).toBeNull();
  });
});

describe("export and import", () => {
  it("exports rows that map onto POST /me/votes:import, newest first", () => {
    const { env } = browser();
    const store = storeIn(env);
    store.setVote("hr-119-1", "yea", "First");
    store.setVote("s-119-2", "skip");
    const file = store.exportVotes();
    expect(file.v).toBe(1);
    expect(file.votes).toEqual([
      { bill_id: "s-119-2", vote: "skip", voted_at: "2026-10-04T15:00:02.000Z", title: undefined },
      { bill_id: "hr-119-1", vote: "yea", voted_at: "2026-10-04T15:00:01.000Z", title: "First" },
    ]);
  });

  it("merges an import, and the newer vote wins", () => {
    const { env } = browser();
    const store = storeIn(env);
    store.setVote("hr-119-1", "yea", "Kept title"); // 15:00:01
    store.setVote("hr-119-2", "yea"); // 15:00:02
    const file = {
      v: 1,
      exported_at: "2026-10-05T00:00:00Z",
      votes: [
        { bill_id: "hr-119-1", vote: "nay", voted_at: "2026-10-05T00:00:00Z" }, // newer: wins
        { bill_id: "hr-119-2", vote: "nay", voted_at: "2026-10-01T00:00:00Z" }, // older: loses
        { bill_id: "hr-119-3", vote: "skip", voted_at: "2026-10-01T00:00:00Z", title: "New" },
      ],
    };
    expect(store.importVotes(JSON.stringify(file))).toEqual({ added: 1, updated: 1, unchanged: 1 });
    const votes = store.getSnapshot().value;
    expect(votes["hr-119-1"]).toEqual({ vote: "nay", at: "2026-10-05T00:00:00.000Z", title: "Kept title" });
    expect(votes["hr-119-2"].vote).toBe("yea");
    expect(votes["hr-119-3"]).toEqual({ vote: "skip", at: "2026-10-01T00:00:00.000Z", title: "New" });
  });

  it("round-trips an export into another browser", () => {
    const from = storeIn(browser().env);
    from.setVote("hr-119-1", "yea", "A");
    from.setVote("hjres-119-7", "nay");
    const to = storeIn(browser().env);
    to.importVotes(JSON.stringify(from.exportVotes()));
    expect(to.getSnapshot().value).toEqual(from.getSnapshot().value);
  });

  it.each([
    ["not JSON", "hello", "isn't valid JSON"],
    ["the store format", JSON.stringify({ v: 1, votes: {} }), "isn't a Just a Bill votes export"],
    ["another version", JSON.stringify({ v: 2, votes: [] }), "isn't a Just a Bill votes export"],
    ["a bad vote", JSON.stringify({ v: 1, votes: [{ bill_id: "hr-119-1", vote: "maybe", voted_at: "2026-10-01" }] }), "Vote 1"],
    ["a bad bill id", JSON.stringify({ v: 1, votes: [{ bill_id: "<b>", vote: "yea", voted_at: "2026-10-01" }] }), "Vote 1"],
    ["a bad time", JSON.stringify({ v: 1, votes: [{ bill_id: "hr-119-1", vote: "yea", voted_at: "soon" }] }), "Vote 1"],
    ["a huge file", "x".repeat(2_000_001), "too large"],
  ])("rejects %s with a message and changes nothing", (_, text, message) => {
    const store = storeIn(browser().env);
    store.setVote("hr-119-1", "yea");
    const before = store.getSnapshot();
    expect(() => store.importVotes(text)).toThrow(VoteImportError);
    expect(() => store.importVotes(text)).toThrow(message);
    expect(store.getSnapshot()).toBe(before);
  });
});

// The API decodes voted_at as RFC 3339 and answers 400 for the whole import batch otherwise (#647).
describe("vote times", () => {
  const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;

  it("stores and exports an imported date-only time as an RFC 3339 time", () => {
    const store = storeIn(browser().env);
    const file = { v: 1, exported_at: "2026-10-05", votes: [{ bill_id: "hr-119-1", vote: "yea", voted_at: "2026-10-04" }] };
    store.importVotes(JSON.stringify(file));
    const want = new Date("2026-10-04").toISOString();
    expect(store.getSnapshot().value["hr-119-1"].at).toBe(want);
    expect(store.exportVotes().votes[0].voted_at).toBe(want);
    const [batch] = importBatches(store.getSnapshot().value);
    expect(batch).toEqual([{ bill_id: "hr-119-1", vote: "yea", voted_at: want }]);
    expect(batch[0].voted_at).toMatch(RFC3339);
  });

  it.each(["2026-10-04T15:00:00", "Oct 4 2026", "2026-10-04T11:00:00-04:00", "2026-10-04T15:00:00Z"])(
    "rewrites %s as toISOString() would",
    (time) => {
      const store = storeIn(browser().env);
      store.importVotes(JSON.stringify({ v: 1, votes: [{ bill_id: "hr-119-1", vote: "nay", voted_at: time }] }));
      const at = store.getSnapshot().value["hr-119-1"].at;
      expect(at).toBe(new Date(time).toISOString());
      expect(at).toMatch(RFC3339);
    }
  );

  it("repairs a time already on the device, without flagging the store", () => {
    const storage = new FakeStorage();
    const raw = JSON.stringify({ v: 1, votes: { "hr-119-1": { vote: "yea", at: "2026-10-04" } } });
    storage.setItem(VOTES_KEY, raw);
    const store = storeIn(browser(storage).env);
    expect(store.getSnapshot().value["hr-119-1"].at).toBe("2026-10-04T00:00:00.000Z");
    expect(storage.getItem(VOTES_CORRUPT_KEY)).toBeNull();
    expect(importBatches(store.getSnapshot().value)[0][0].voted_at).toBe("2026-10-04T00:00:00.000Z");
  });

  it("drops a stored vote whose time can't be read or has no RFC 3339 form", () => {
    const storage = new FakeStorage();
    storage.setItem(
      VOTES_KEY,
      JSON.stringify({
        v: 1,
        votes: {
          "hr-119-1": { vote: "yea", at: "2026-10-04T00:00:00Z" },
          "hr-119-2": { vote: "yea", at: "soon" },
          "hr-119-3": { vote: "yea", at: "+010000-01-01T00:00:00Z" },
          "hr-119-4": { vote: "yea", at: "-000001-01-01T00:00:00Z" },
        },
      })
    );
    const store = storeIn(browser(storage).env);
    expect(Object.keys(store.getSnapshot().value)).toEqual(["hr-119-1"]);
  });

  it("rejects a file with a time that has no RFC 3339 form", () => {
    const store = storeIn(browser().env);
    const file = { v: 1, votes: [{ bill_id: "hr-119-1", vote: "yea", voted_at: "+010000-01-01T00:00:00Z" }] };
    expect(() => store.importVotes(JSON.stringify(file))).toThrow("Vote 1");
    expect(store.getSnapshot().value).toEqual({});
  });
});
