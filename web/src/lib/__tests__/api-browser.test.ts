// @vitest-environment jsdom
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";

// #970: the browser calls api.justabill.io, another origin, so a GET must stay a CORS "simple"
// request (no Content-Type, no preflight), and every call must give up on an API that stalls.

const mockFetch = vi.fn();

function ok(body: object) {
  return { ok: true, status: 200, statusText: "OK", json: () => Promise.resolve(body) };
}

let api: typeof import("../api");

beforeAll(async () => {
  vi.stubGlobal("fetch", mockFetch);
  vi.resetModules();
  api = await import("../api");
});

afterEach(() => {
  mockFetch.mockReset();
  vi.restoreAllMocks();
});

afterAll(() => {
  vi.unstubAllGlobals();
});

describe("browser calls to the API (#970)", () => {
  it("sends a GET with no Content-Type, and a signal that times it out after 10 s", async () => {
    const timeout = vi.spyOn(AbortSignal, "timeout");
    mockFetch.mockResolvedValueOnce(ok({ items: [], total: 0, offset: 0, limit: 20 }));

    await api.listBills();

    const init = mockFetch.mock.calls[0][1] as RequestInit;
    expect(Object.keys(init.headers as Record<string, string>).map((h) => h.toLowerCase())).not.toContain(
      "content-type"
    );
    expect(timeout).toHaveBeenCalledExactlyOnceWith(10_000);
    expect(init.signal).toBe(timeout.mock.results[0].value);
  });

  it("gives a POST 30 s, past the API's own deadline, and keeps its JSON Content-Type", async () => {
    const timeout = vi.spyOn(AbortSignal, "timeout");
    mockFetch.mockResolvedValueOnce(ok({ reps: [], senators: [], districts: [] }));

    await api.getReps("1100 Congress Ave, Austin, TX 78701");

    const init = mockFetch.mock.calls[0][1] as RequestInit;
    expect((init.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
    expect(timeout).toHaveBeenCalledExactlyOnceWith(30_000);
    expect(init.signal).toBe(timeout.mock.results[0].value);
  });

  it("rejects with the abort's TimeoutError when the signal fires", async () => {
    const controller = new AbortController();
    vi.spyOn(AbortSignal, "timeout").mockReturnValue(controller.signal);
    mockFetch.mockImplementationOnce(
      (_url: string, init: RequestInit) =>
        new Promise((_, reject) => init.signal?.addEventListener("abort", () => reject(init.signal?.reason)))
    );

    const call = api.getBill("hr-119-1");
    controller.abort(new DOMException("signal timed out", "TimeoutError"));

    await expect(call).rejects.toMatchObject({ name: "TimeoutError" });
  });
});
