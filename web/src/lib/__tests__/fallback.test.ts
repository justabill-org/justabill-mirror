import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api";
import { fixturesAllowed, isBuildPhase, isNotFound, orDevFixture, previewExamplesOn } from "../fallback";

const DEV = { NODE_ENV: "development" };
const PROD = { NODE_ENV: "production" };
// Vercel builds and serves previews and production with NODE_ENV=production.
const PREVIEW = { NODE_ENV: "production", VERCEL_ENV: "preview" };
const VERCEL_PROD = { NODE_ENV: "production", VERCEL_ENV: "production" };

afterEach(() => {
  vi.restoreAllMocks();
});

describe("fixturesAllowed", () => {
  it("is true under next dev, and false elsewhere off Vercel", () => {
    expect(fixturesAllowed(DEV)).toBe(true);
    expect(fixturesAllowed(PROD)).toBe(false);
    expect(fixturesAllowed({ NODE_ENV: "test" })).toBe(false);
    expect(fixturesAllowed({})).toBe(false);
  });

  it("reads process.env by default", () => {
    // Vitest runs with NODE_ENV=test.
    expect(fixturesAllowed()).toBe(false);
  });

  it("is true on Vercel previews and never in Vercel production (#678)", () => {
    expect(fixturesAllowed(PREVIEW)).toBe(true);
    expect(fixturesAllowed(VERCEL_PROD)).toBe(false);
    expect(fixturesAllowed({ NODE_ENV: "production", VERCEL_ENV: "development" })).toBe(false);
  });
});

describe("previewExamplesOn", () => {
  it("is on for Vercel previews only", () => {
    expect(previewExamplesOn(PREVIEW)).toBe(true);
    expect(previewExamplesOn(VERCEL_PROD)).toBe(false);
    expect(previewExamplesOn(DEV)).toBe(false);
    expect(previewExamplesOn({})).toBe(false);
  });

  it("turns off with PREVIEW_EXAMPLE_DATA=off, for go-public", () => {
    expect(previewExamplesOn({ ...PREVIEW, PREVIEW_EXAMPLE_DATA: "off" })).toBe(false);
    expect(previewExamplesOn({ ...PREVIEW, PREVIEW_EXAMPLE_DATA: " OFF " })).toBe(false);
    expect(fixturesAllowed({ ...PREVIEW, PREVIEW_EXAMPLE_DATA: "off" })).toBe(false);
    expect(previewExamplesOn({ ...PREVIEW, PREVIEW_EXAMPLE_DATA: "on" })).toBe(true);
    expect(previewExamplesOn({ ...PREVIEW, PREVIEW_EXAMPLE_DATA: "" })).toBe(true);
  });

  it("reads process.env by default", () => {
    vi.stubEnv("VERCEL_ENV", "preview");
    expect(previewExamplesOn()).toBe(true);
    vi.stubEnv("VERCEL_ENV", "production");
    expect(previewExamplesOn()).toBe(false);
  });
});

describe("isBuildPhase", () => {
  it("is true only while next build prerenders", () => {
    expect(isBuildPhase({ NEXT_PHASE: "phase-production-build" })).toBe(true);
    expect(isBuildPhase({ NEXT_PHASE: "phase-production-server" })).toBe(false);
    expect(isBuildPhase({})).toBe(false);
  });
});

describe("isNotFound", () => {
  it("matches the API's 404 and 400", () => {
    expect(isNotFound(new ApiError(404, "Not Found", ""))).toBe(true);
    expect(isNotFound(new ApiError(400, "Bad Request", ""))).toBe(true);
  });

  it("doesn't match other failures", () => {
    expect(isNotFound(new ApiError(500, "Internal Server Error", ""))).toBe(false);
    expect(isNotFound(new ApiError(429, "Too Many Requests", ""))).toBe(false);
    expect(isNotFound(new TypeError("fetch failed"))).toBe(false);
    expect(isNotFound(undefined)).toBe(false);
  });
});

describe("orDevFixture", () => {
  it("resolves to the API's data when the request succeeds", async () => {
    await expect(orDevFixture(Promise.resolve("real"), "fixture", DEV)).resolves.toBe("real");
    await expect(orDevFixture(Promise.resolve("real"), "fixture", PROD)).resolves.toBe("real");
  });

  it("rethrows outside next dev, so the error page renders", async () => {
    const err = new ApiError(503, "Service Unavailable", "");
    await expect(orDevFixture(Promise.reject(err), "fixture", PROD)).rejects.toBe(err);
    const network = new TypeError("fetch failed");
    await expect(orDevFixture(Promise.reject(network), "fixture", PROD)).rejects.toBe(network);
  });

  it("rethrows under vitest's NODE_ENV by default", async () => {
    await expect(orDevFixture(Promise.reject(new TypeError("fetch failed")), "fixture")).rejects.toThrow(
      "fetch failed"
    );
  });

  it("falls back to the fixture under next dev", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    await expect(orDevFixture(Promise.reject(new TypeError("fetch failed")), "fixture", DEV)).resolves.toBe(
      "fixture"
    );
    await expect(
      orDevFixture(Promise.reject(new ApiError(500, "Internal Server Error", "")), "fixture", DEV)
    ).resolves.toBe("fixture");
    expect(warn).toHaveBeenCalledTimes(2);
  });

  it("rethrows not found even under next dev", async () => {
    const err = new ApiError(404, "Not Found", "");
    await expect(orDevFixture(Promise.reject(err), "fixture", DEV)).rejects.toBe(err);
  });
});

describe("orDevFixture on Vercel (#678)", () => {
  const failures = [
    ["the private API's 404", new ApiError(404, "Not Found", "")],
    ["a 403", new ApiError(403, "Forbidden", "")],
    ["a network error", new TypeError("fetch failed")],
    ["a 500", new ApiError(500, "Internal Server Error", "")],
  ] as const;

  it.each(failures)("falls back to the fixture on a preview after %s", async (_, err) => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    await expect(orDevFixture(Promise.reject(err), "fixture", PREVIEW)).resolves.toBe("fixture");
    expect(warn).toHaveBeenCalledOnce();
  });

  it.each(failures)("rethrows %s in production", async (_, err) => {
    await expect(orDevFixture(Promise.reject(err), "fixture", VERCEL_PROD)).rejects.toBe(err);
  });

  it("rethrows on a preview with PREVIEW_EXAMPLE_DATA=off", async () => {
    const err = new ApiError(404, "Not Found", "");
    await expect(orDevFixture(Promise.reject(err), "fixture", { ...PREVIEW, PREVIEW_EXAMPLE_DATA: "off" })).rejects.toBe(
      err
    );
  });

  it("still rethrows a malformed ID's 400 on a preview", async () => {
    const err = new ApiError(400, "Bad Request", "");
    await expect(orDevFixture(Promise.reject(err), "fixture", PREVIEW)).rejects.toBe(err);
  });

  it("resolves to the API's data on a preview when the request succeeds", async () => {
    await expect(orDevFixture(Promise.resolve("real"), "fixture", PREVIEW)).resolves.toBe("real");
  });
});
