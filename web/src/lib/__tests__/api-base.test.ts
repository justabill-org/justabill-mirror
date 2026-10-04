import { afterEach, describe, expect, it, vi } from "vitest";

import { apiBaseUrl } from "../api";

// #649: in `task up` the web container's server renders called its own localhost:8080 instead of
// the api service, so every page rendered its error state.

const mockFetch = vi.fn();

function okHealth() {
  return { ok: true, status: 200, json: () => Promise.resolve({ status: "ok" }) };
}

/** Loads a fresh lib/api.ts, so API_BASE is computed from the env stubbed before it. */
async function freshApi() {
  vi.resetModules();
  vi.stubGlobal("fetch", mockFetch);
  return import("../api");
}

afterEach(() => {
  mockFetch.mockReset();
});

describe("apiBaseUrl", () => {
  const urls = { publicUrl: "http://localhost:8080", internalUrl: "http://api:8080" };

  it("prefers the internal URL on the server", () => {
    expect(apiBaseUrl(urls, true)).toBe("http://api:8080");
  });

  it("always uses the public URL in the browser", () => {
    expect(apiBaseUrl(urls, false)).toBe("http://localhost:8080");
  });

  it("falls back to the public URL on the server when no internal URL is set (Vercel)", () => {
    expect(apiBaseUrl({ publicUrl: "https://api.justabill.io" }, true)).toBe("https://api.justabill.io");
    expect(apiBaseUrl({ publicUrl: "https://api.justabill.io", internalUrl: "" }, true)).toBe(
      "https://api.justabill.io"
    );
  });

  it("defaults to localhost:8080 with neither set", () => {
    expect(apiBaseUrl({}, true)).toBe("http://localhost:8080");
    expect(apiBaseUrl({}, false)).toBe("http://localhost:8080");
  });
});

describe("server renders", () => {
  it("call API_INTERNAL_URL when it's set, as in the compose web container", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "http://localhost:8080");
    vi.stubEnv("API_INTERNAL_URL", "http://api:8080");
    mockFetch.mockResolvedValueOnce(okHealth());

    const { getHealth } = await freshApi();
    await getHealth();

    expect(mockFetch.mock.calls[0]?.[0]).toBe("http://api:8080/health");
  });

  it("call NEXT_PUBLIC_API_URL when API_INTERNAL_URL is unset", async () => {
    vi.stubEnv("NEXT_PUBLIC_API_URL", "https://api.justabill.io");
    vi.stubEnv("API_INTERNAL_URL", undefined);
    mockFetch.mockResolvedValueOnce(okHealth());

    const { getHealth } = await freshApi();
    await getHealth();

    expect(mockFetch.mock.calls[0]?.[0]).toBe("https://api.justabill.io/health");
  });
});
