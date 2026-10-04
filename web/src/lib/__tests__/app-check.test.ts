import { afterEach, describe, expect, it, vi } from "vitest";
import { APP_CHECK_HEADER, appCheckHeaders, needsAppCheck, setAppCheckSource } from "../auth/app-check";

// Which calls carry X-Firebase-AppCheck (#122), and how a write goes on without a token.

afterEach(() => {
  setAppCheckSource(null);
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("needsAppCheck", () => {
  it.each([
    ["POST", "/bills/hr-119-1/vote", true],
    ["post", "/bills/hr-119-1/vote", true],
    ["POST", "/me/votes:import", true],
    ["POST", "/me/votes:import?dry_run=1", true],
    ["GET", "/bills/hr-119-1/vote", false],
    [undefined, "/me/votes:import", false],
    ["POST", "/me", false],
    ["PATCH", "/me", false],
    ["POST", "/me/favorites/hr-119-1", false],
    ["POST", "/bills/hr-119-1/vote/extra", false],
    ["POST", "/reps", false],
  ])("%s %s → %s", (method, path, want) => {
    expect(needsAppCheck(method, path)).toBe(want);
  });
});

describe("appCheckHeaders", () => {
  it("is empty with no source, e.g. on the server or with sign-in off", async () => {
    await expect(appCheckHeaders()).resolves.toEqual({});
  });

  it("carries the source's token, and nothing when it has none", async () => {
    setAppCheckSource(async () => "token-1");
    await expect(appCheckHeaders()).resolves.toEqual({ [APP_CHECK_HEADER]: "token-1" });
    setAppCheckSource(async () => null);
    await expect(appCheckHeaders()).resolves.toEqual({});
  });

  it("is empty, with a warning, when App Check fails", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    setAppCheckSource(() => Promise.reject(new Error("exchange refused")));
    await expect(appCheckHeaders()).resolves.toEqual({});
    expect(warn).toHaveBeenCalledOnce();
  });

  it("stops waiting after the timeout", async () => {
    vi.useFakeTimers();
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    setAppCheckSource(() => new Promise(() => {}));
    const headers = appCheckHeaders(1000);
    await vi.advanceTimersByTimeAsync(1000);
    await expect(headers).resolves.toEqual({});
    expect(warn).toHaveBeenCalledWith(expect.stringContaining("1000 ms"));
  });
});
