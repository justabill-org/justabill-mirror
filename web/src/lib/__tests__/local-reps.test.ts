import { describe, expect, it } from "vitest";
import type { LocalEnv, StorageLike } from "../local/storage";
import { REPS_CORRUPT_KEY, REPS_KEY, createRepsStore, type LocalReps } from "../local/reps";

function memoryStorage(): StorageLike & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => void data.set(k, v),
    removeItem: (k) => void data.delete(k),
  };
}

const reps: LocalReps = {
  state: "NY",
  district: 12,
  looked_up_at: "2026-10-04T15:00:00Z",
  members: [
    { id: "N000002", name: "Jerrold Nadler", party: "D", chamber: "House", state: "NY", district: 12 },
    { id: "S000148", name: "Charles E. Schumer", party: "D", chamber: "Senate", state: "NY" },
  ],
};

describe("local reps store", () => {
  it("is empty on the server", () => {
    const store = createRepsStore();
    expect(store.getSnapshot()).toMatchObject({ value: null, persistence: "server" });
  });

  it("saves and reloads jab.reps.v1 without any extra fields such as an address", () => {
    const storage = memoryStorage();
    const env: LocalEnv = { storage, events: null };
    const store = createRepsStore({ env: () => env });
    store.saveReps({ ...reps, address: "123 Main St" } as LocalReps);

    const stored = JSON.parse(storage.getItem(REPS_KEY)!);
    expect(stored).toEqual({ v: 1, ...reps });
    expect(JSON.stringify(stored)).not.toContain("Main St");
    expect(createRepsStore({ env: () => env }).getSnapshot().value).toEqual(reps);
  });

  it("keeps the next election's districts, and drops a malformed election block", () => {
    const election = {
      congress: 120,
      election_date: "2026-11-03",
      districts: [{ state: "TX", district: 10 }],
      current: [{ state: "TX", district: 37 }],
    };
    const storage = memoryStorage();
    const env: LocalEnv = { storage, events: null };
    const store = createRepsStore({ env: () => env });
    store.saveReps({ ...reps, election });
    expect(createRepsStore({ env: () => env }).getSnapshot().value?.election).toEqual(election);

    for (const bad of [
      { ...election, election_date: "soon" },
      { ...election, congress: "120" },
      { ...election, districts: [] },
      { ...election, districts: [{ state: "TX", district: null }] },
      { ...election, current: "TX-37" },
    ]) {
      store.saveReps({ ...reps, election: bad } as unknown as LocalReps);
      const got = createRepsStore({ env: () => env }).getSnapshot().value;
      expect(got).toEqual(reps);
    }
  });

  it("clears to empty rather than corrupt", () => {
    const storage = memoryStorage();
    const env: LocalEnv = { storage, events: null };
    const store = createRepsStore({ env: () => env });
    store.saveReps(reps);
    store.clearReps();
    const reloaded = createRepsStore({ env: () => env });
    expect(reloaded.getSnapshot()).toMatchObject({ value: null, persistence: "device" });
    expect(storage.getItem(REPS_CORRUPT_KEY)).toBeNull();
  });

  it("rejects invalid reps", () => {
    const store = createRepsStore({ env: () => ({ storage: memoryStorage(), events: null }) });
    expect(() => store.saveReps({ ...reps, district: -1 })).toThrow();
    expect(() => store.saveReps({ ...reps, members: [{ id: "", name: "x", party: "", chamber: "House", state: "NY" }] })).toThrow();
  });

  it("moves an unreadable value aside", () => {
    const storage = memoryStorage();
    storage.setItem(REPS_KEY, JSON.stringify({ v: 9 }));
    const store = createRepsStore({ env: () => ({ storage, events: null }) });
    expect(store.getSnapshot().value).toBeNull();
    expect(storage.getItem(REPS_CORRUPT_KEY)).toBe(JSON.stringify({ v: 9 }));
  });
});
