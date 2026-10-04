import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api";
import { deleteAccount, type DeletingUser } from "../auth/delete-account";
import { afterSignInPath, onboarded, ONBOARDED_KEY, setOnboarded } from "../auth/onboarding";
import type { LocalEnv } from "../local/storage";
import type { LocalVotes } from "../local/votes";
import type { User } from "../types";
import { importBatches, importLocalVotes, type ImportCall } from "../votes/import";

// Onboarding, vote import and account deletion (#138), without the UI.

function fakeEnv(): LocalEnv & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return {
    data,
    storage: {
      getItem: (k) => data.get(k) ?? null,
      setItem: (k, v) => void data.set(k, v),
      removeItem: (k) => void data.delete(k),
    },
    events: null,
  };
}

function votes(n: number): LocalVotes {
  const out: Record<string, LocalVotes[string]> = {};
  // Newest first, so the batches have to sort them.
  for (let i = 0; i < n; i++) {
    out[`hr-119-${i + 1}`] = { vote: i % 3 === 0 ? "skip" : "yea", at: new Date(Date.UTC(2026, 9, 1) - i * 60_000).toISOString(), title: `Bill ${i + 1}` };
  }
  return out;
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("importBatches", () => {
  it("sends every vote, skips included, oldest first, without titles, 1,000 to a batch", () => {
    const batches = importBatches(votes(2_345));
    expect(batches.map((b) => b.length)).toEqual([1000, 1000, 345]);
    const all = batches.flat();
    expect(all[0].bill_id).toBe("hr-119-2345");
    expect(all.at(-1)).toEqual({ bill_id: "hr-119-1", vote: "skip", voted_at: "2026-10-01T00:00:00.000Z" });
    expect(Object.keys(all[0])).toEqual(["bill_id", "vote", "voted_at"]);
  });

  it("has no batches without votes", () => {
    expect(importBatches({})).toEqual([]);
  });
});

describe("importLocalVotes", () => {
  it("adds up the batches, with a fresh token for each", async () => {
    const call = vi.fn<ImportCall>(async (_t, rows) => ({ imported: rows.length - 1, skipped: 1 }));
    const token = vi.fn(async () => "tok");
    expect(await importLocalVotes(votes(1_500), token, call)).toEqual({ imported: 1_498, skipped: 2, kept: [] });
    expect(call).toHaveBeenCalledTimes(2);
    expect(token).toHaveBeenCalledTimes(2);
  });

  it("stops once the daily cap holds votes back, and keeps those and every unsent vote", async () => {
    const call = vi.fn<ImportCall>(async (_t, rows) => ({
      imported: 990,
      skipped: 0,
      capped: rows.length - 990,
      capped_bill_ids: rows.slice(990).map((r) => r.bill_id),
    }));
    const totals = await importLocalVotes(votes(2_345), async () => "tok", call);
    expect(call).toHaveBeenCalledTimes(1);
    expect(totals.imported).toBe(990);
    expect(totals.kept).toHaveLength(10 + 1_345);
    const sent = call.mock.calls[0][1].map((r) => r.bill_id);
    expect(totals.kept.slice(0, 10)).toEqual(sent.slice(990));
    expect(new Set([...sent.slice(0, 990), ...totals.kept]).size).toBe(2_345);
  });

  it("rejects when a batch fails", async () => {
    const call = vi.fn<ImportCall>().mockResolvedValueOnce({ imported: 1000, skipped: 0 }).mockRejectedValueOnce(new Error("503"));
    await expect(importLocalVotes(votes(1_001), async () => "tok", call)).rejects.toThrow("503");
  });
});

describe("onboarding", () => {
  const fresh: User = { id: "u-1", created_at: "2026-10-05T00:00:00Z" };
  const placed: User = { ...fresh, state: "CA", district: 12 };
  const never = () => false;

  it("sends a new account without a district through onboarding, keeping ?next=", () => {
    expect(afterSignInPath(fresh, "/bills?congress=119", 0, never)).toBe("/signup?next=%2Fbills%3Fcongress%3D119");
  });

  it("sends an account with a district there only when this device has votes to offer", () => {
    expect(afterSignInPath(placed, "/vote", 0, never)).toBe("/vote");
    expect(afterSignInPath(placed, "/vote", 3, never)).toBe("/signup?next=%2Fvote");
  });

  it("skips it once the account went through it on this device", () => {
    expect(afterSignInPath(fresh, "/vote", 3, (id) => id === "u-1")).toBe("/vote");
    expect(afterSignInPath(fresh, "/vote", 3, (id) => id === "u-2")).toBe("/signup?next=%2Fvote");
  });

  it("remembers the account that finished onboarding here, and forgets it", () => {
    const env = fakeEnv();
    expect(onboarded("u-1", () => env)).toBe(false);
    setOnboarded("u-1", () => env);
    expect(env.data.get(ONBOARDED_KEY)).toBe("u-1");
    expect(onboarded("u-1", () => env)).toBe(true);
    expect(onboarded("u-2", () => env)).toBe(false);
    setOnboarded(null, () => env);
    expect(env.data.has(ONBOARDED_KEY)).toBe(false);
  });

  it("gets by without storage", () => {
    const throwing: LocalEnv = {
      storage: {
        getItem: () => {
          throw new Error("blocked");
        },
        setItem: () => {
          throw new Error("full");
        },
        removeItem: () => {},
      },
      events: null,
    };
    expect(onboarded("u-1", () => throwing)).toBe(false);
    expect(() => setOnboarded("u-1", () => throwing)).not.toThrow();
    expect(onboarded("u-1", () => null)).toBe(false);
  });
});

describe("deleteAccount", () => {
  function user(): DeletingUser & { [K in keyof DeletingUser]: ReturnType<typeof vi.fn> } {
    let n = 0;
    return {
      getIdToken: vi.fn(async () => `tok-${++n}`),
      reauthenticate: vi.fn(async () => {}),
      signOut: vi.fn(async () => {}),
    };
  }
  const recent = new ApiError(401, "Unauthorized", '{"code":"requires_recent_login"}');

  it("deletes with DELETE /me and signs out", async () => {
    const u = user();
    const del = vi.fn(async () => {});
    await deleteAccount(u, del);
    expect(del).toHaveBeenCalledWith("tok-1");
    expect(u.reauthenticate).not.toHaveBeenCalled();
    expect(u.signOut).toHaveBeenCalled();
  });

  it("asks for a fresh sign-in when the API wants one, then tries once more with a new token", async () => {
    const u = user();
    const del = vi.fn<(t: string) => Promise<void>>().mockRejectedValueOnce(recent).mockResolvedValueOnce();
    await deleteAccount(u, del);
    expect(u.reauthenticate).toHaveBeenCalledTimes(1);
    expect(del.mock.calls).toEqual([["tok-1"], ["tok-2"]]);
    expect(u.signOut).toHaveBeenCalled();
  });

  it("keeps the account and stays signed in when the re-sign-in is cancelled or the API fails", async () => {
    const cancelled = user();
    cancelled.reauthenticate.mockRejectedValueOnce({ code: "auth/popup-closed-by-user" });
    const del = vi.fn<(t: string) => Promise<void>>().mockRejectedValue(recent);
    await expect(deleteAccount(cancelled, del)).rejects.toEqual({ code: "auth/popup-closed-by-user" });
    expect(del).toHaveBeenCalledTimes(1);
    expect(cancelled.signOut).not.toHaveBeenCalled();

    const down = user();
    const err = new ApiError(503, "Service Unavailable", '{"code":"auth_unavailable"}');
    await expect(deleteAccount(down, vi.fn(async () => Promise.reject(err)))).rejects.toBe(err);
    expect(down.reauthenticate).not.toHaveBeenCalled();
    expect(down.signOut).not.toHaveBeenCalled();
  });
});
