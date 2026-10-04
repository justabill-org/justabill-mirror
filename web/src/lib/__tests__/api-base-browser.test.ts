// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";

// #649: API_INTERNAL_URL is for server renders only; the browser on the host keeps calling the
// public URL, even if the variable leaks into its environment.

const mockFetch = vi.fn();

afterEach(() => {
  mockFetch.mockReset();
});

it("calls NEXT_PUBLIC_API_URL from the browser, never API_INTERNAL_URL", async () => {
  vi.stubEnv("NEXT_PUBLIC_API_URL", "http://localhost:8080");
  vi.stubEnv("API_INTERNAL_URL", "http://api:8080");
  vi.stubGlobal("fetch", mockFetch);
  mockFetch.mockResolvedValueOnce({ ok: true, status: 200, json: () => Promise.resolve({ status: "ok" }) });

  vi.resetModules();
  const { getHealth } = await import("../api");
  await getHealth();

  expect(mockFetch.mock.calls[0]?.[0]).toBe("http://localhost:8080/health");
});
