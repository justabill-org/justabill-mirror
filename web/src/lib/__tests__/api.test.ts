import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Mock fetch globally before importing api module
const mockFetch = vi.fn();
vi.stubGlobal("fetch", mockFetch);

// Tests resolve `#server-key` without the react-server condition, so they'd get the no-op; load the
// server components' version, which sends X-Server-Key only when API_SERVER_KEY is set.
vi.mock("#server-key", () => import("../server-key"));
// The request a server render is answering, as Vercel hands it over (withVisitor reads x-real-ip).
vi.mock("next/headers", () => ({ headers: async () => new Headers({ "x-real-ip": "203.0.113.7" }) }));

const { setAppCheckSource } = await import("../auth/app-check");

// Now import after mock is set up
const {
  listBills,
  getBill,
  castVote,
  deleteVote,
  getScorecard,
  createMe,
  getMe,
  updateMe,
  getCompanionVotes,
  getBillLawChanges,
  getLawSection,
  getRelatedBills,
  getCollaborators,
  getMemberPositions,
  getMember,
  getBillText,
  listTextVersions,
  listDiffs,
  getDiff,
  listAmendments,
  listGAOReports,
  listBillsWithSummaries,
  countBills,
  listCongresses,
  listPolicyAreas,
  getMyVotes,
  getReps,
  getRepsAt,
  exportMe,
  deleteMe,
  importMyVotes,
  ApiError,
  REVALIDATE,
  CACHE_TAGS,
  isPartialBillDetail,
  PARTIAL_BILL_RETRY_HEADER,
} = await import("../api");
const { isNotFound } = await import("../fallback");

function mockResponse(data: object, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? "OK" : "Error",
    json: () => Promise.resolve(data),
    text: () => Promise.resolve(JSON.stringify(data)),
  };
}

beforeEach(() => {
  mockFetch.mockReset();
});

describe("listBills", () => {
  it("calls /bills with default params", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ items: [], total: 0, offset: 0, limit: 20 }));
    const result = await listBills();
    expect(result.items).toEqual([]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    const url = mockFetch.mock.calls[0][0] as string;
    expect(url).toContain("/api/v1/bills");
  });

  it("passes filter params as query string", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ items: [], total: 0, offset: 0, limit: 20 }));
    await listBills({ status: "became_law", sort: "updated_at", congress: 119 });
    const url = mockFetch.mock.calls[0][0] as string;
    expect(url).toContain("status=became_law");
    expect(url).toContain("sort=updated_at");
    expect(url).toContain("congress=119");
  });

  it("passes status_mode for journey filter", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ items: [], total: 0, offset: 0, limit: 20 }));
    await listBills({ status: "passed_house", status_mode: "past" });
    const url = mockFetch.mock.calls[0][0] as string;
    expect(url).toContain("status=passed_house");
    expect(url).toContain("status_mode=past");
  });

  it("passes unvoted=true and the ID token as a Bearer header", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ items: [], total: 0, offset: 0, limit: 20 }));
    await listBills({ unvoted: true }, "id-token-123");
    const url = mockFetch.mock.calls[0][0] as string;
    expect(url).toContain("unvoted=true");
    const headers = mockFetch.mock.calls[0][1]?.headers as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer id-token-123");
    expect(headers["X-Dev-User-Id"]).toBeUndefined();
  });

  it("omits undefined params from query string", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ items: [], total: 0, offset: 0, limit: 20 }));
    await listBills({ status: "in_committee" });
    const url = mockFetch.mock.calls[0][0] as string;
    expect(url).not.toContain("congress=");
    expect(url).not.toContain("sort=");
    expect(url).not.toContain("unvoted=");
  });
});

describe("getBill", () => {
  it("calls /bills/:id", async () => {
    const detail = { bill: { id: "hr-119-1" }, actions: [], status_history: [] };
    mockFetch.mockResolvedValueOnce(mockResponse(detail));
    const result = await getBill("hr-119-1");
    expect(result.bill.id).toBe("hr-119-1");
    const url = mockFetch.mock.calls[0][0] as string;
    expect(url).toContain("/api/v1/bills/hr-119-1");
  });
});

describe("caching (#74)", () => {
  it("caches a bill for REVALIDATE.bill, tagged with the bill", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ bill: { id: "hr-119-1" } }));
    await getBill("hr-119-1");
    const opts = mockFetch.mock.calls[0][1];
    expect(opts.next).toEqual({ revalidate: REVALIDATE.bill, tags: ["bills", "bill:hr-119-1"] });
    expect(opts.headers.Authorization).toBeUndefined();
  });

  it("reads a partial bill again under its own cache key for REVALIDATE.partialBill (#454)", async () => {
    const partial = { bill: { id: "hr-119-1" }, actions: [], votes: null };
    const whole = { bill: { id: "hr-119-1" }, actions: [], votes: [] };
    mockFetch.mockResolvedValueOnce(mockResponse(partial)).mockResolvedValueOnce(mockResponse(whole));
    const result = await getBill("hr-119-1");
    expect(result).toEqual(whole);
    expect(mockFetch).toHaveBeenCalledTimes(2);
    const [firstUrl, first] = mockFetch.mock.calls[0];
    const [retryUrl, retry] = mockFetch.mock.calls[1];
    expect(retryUrl).toBe(firstUrl);
    expect(first.headers[PARTIAL_BILL_RETRY_HEADER]).toBeUndefined();
    expect(retry.headers[PARTIAL_BILL_RETRY_HEADER]).toBe("1");
    expect(retry.next).toEqual({ revalidate: REVALIDATE.partialBill, tags: ["bills", "bill:hr-119-1"] });
    expect(REVALIDATE.partialBill).toBeLessThan(REVALIDATE.bill);
  });

  it("returns the retry even when it is partial too, so the page still renders", async () => {
    const partial = { bill: { id: "hr-119-1" }, actions: null };
    mockFetch.mockResolvedValue(mockResponse(partial));
    expect(await getBill("hr-119-1")).toEqual(partial);
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("reads a whole bill once", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ bill: { id: "hr-119-1" }, actions: [], sponsorships: [] }));
    await getBill("hr-119-1");
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("caches public lists but never a user's list", async () => {
    mockFetch.mockResolvedValue(mockResponse({ items: [], total: 0, offset: 0, limit: 20 }));
    await listBills({ congress: 119 });
    await listBills({ unvoted: true }, "id-token-123");
    expect(mockFetch.mock.calls[0][1].next).toEqual({ revalidate: REVALIDATE.list, tags: [CACHE_TAGS.bills] });
    const personal = mockFetch.mock.calls[1][1];
    expect(personal.next).toBeUndefined();
    expect(personal.cache).toBe("no-store");
    expect(personal.headers.Authorization).toBe("Bearer id-token-123");
  });

  it("caches members and congresses for REVALIDATE.member", async () => {
    mockFetch.mockResolvedValue(mockResponse([]));
    await getMember("A000001");
    await listCongresses();
    expect(mockFetch.mock.calls[0][1].next).toEqual({
      revalidate: REVALIDATE.member,
      tags: ["members", "member:A000001"],
    });
    expect(mockFetch.mock.calls[1][1].next).toEqual({ revalidate: REVALIDATE.member, tags: ["congresses"] });
  });

  it("doesn't cache user reads", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ items: [], total: 0, offset: 0, limit: 100 }));
    await getMyVotes("id-token-123", { limit: 100 });
    expect(mockFetch.mock.calls[0][1].next).toBeUndefined();
  });
});

describe("listBillsWithSummaries", () => {
  it("asks for the AI and CRS summaries in the one list call (#714)", async () => {
    mockFetch.mockResolvedValue(mockResponse({ items: [], total: 0, offset: 0, limit: 30 }));
    await listBillsWithSummaries({ status: "became_law", limit: 30 });
    await listBillsWithSummaries();
    expect(mockFetch.mock.calls[0][0]).toMatch(/\/bills\?limit=30&status=became_law&include=summary,crs_summary$/);
    expect(mockFetch.mock.calls[1][0]).toMatch(/\/bills\?include=summary,crs_summary$/);
    expect(mockFetch.mock.calls[0][1].next).toEqual({ revalidate: REVALIDATE.list, tags: ["bills"] });
  });

  it("sends several statuses comma-separated, in one call (#712)", async () => {
    mockFetch.mockResolvedValue(mockResponse({ items: [], total: 0, offset: 0, limit: 12 }));
    await listBillsWithSummaries({ status: ["became_law", "signed"], sort: "latest_action" });
    expect(mockFetch.mock.calls).toHaveLength(1);
    expect(mockFetch.mock.calls[0][0]).toMatch(/\/bills\?status=became_law%2Csigned&sort=latest_action&include=/);
  });
});

describe("countBills (#713)", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("reads GET /bills/counts with the filters alone, cached like the list", async () => {
    mockFetch.mockResolvedValue(mockResponse({ by_status: { became_law: 3 }, total: 9 }));
    const counts = await countBills({
      congress: 119,
      type: "hr",
      chamber: "senate",
      status: ["became_law", "signed"],
      sort: "latest_action",
      offset: 24,
      limit: 12,
    });
    expect(counts).toEqual({ by_status: { became_law: 3 }, total: 9 });
    expect(mockFetch.mock.calls[0][0]).toMatch(/\/api\/v1\/bills\/counts\?congress=119&type=hr&chamber=senate$/);
    expect(mockFetch.mock.calls[0][1].next).toEqual({ revalidate: REVALIDATE.list, tags: [CACHE_TAGS.bills] });
  });

  it("counts a search against the visitor on Vercel when asked", async () => {
    vi.stubEnv("VERCEL", "1");
    mockFetch.mockResolvedValue(mockResponse({ by_status: {}, total: 0 }));
    await countBills({ q: "tax" }, { visitor: true });
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toMatch(/\/bills\/counts\?q=tax$/);
    expect(init.cache).toBe("no-store");
    expect(init.headers).toHaveProperty("X-Visitor-IP", "203.0.113.7");
  });
});

describe("policy areas (#708)", () => {
  it("filters a list by policy area", async () => {
    mockFetch.mockResolvedValue(mockResponse({ items: [], total: 0, offset: 0, limit: 30 }));
    await listBillsWithSummaries({ policy_area: "Armed Forces and National Security", limit: 30 });
    expect(mockFetch.mock.calls[0][0]).toMatch(
      /\/bills\?limit=30&policy_area=Armed%20Forces%20and%20National%20Security&include=summary,crs_summary$/
    );
  });

  it("reads GET /policy-areas, cached like the members", async () => {
    mockFetch.mockResolvedValue(mockResponse({ policy_areas: ["Health"] }));
    expect(await listPolicyAreas()).toEqual({ policy_areas: ["Health"] });
    expect(mockFetch.mock.calls[0][0]).toMatch(/\/api\/v1\/policy-areas$/);
    expect(mockFetch.mock.calls[0][1].next).toEqual({ revalidate: REVALIDATE.member, tags: ["bills"] });
  });
});

describe("castVote", () => {
  it("sends POST with vote body and the ID token", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ bill_id: "hr-119-1", vote: "yea", status: "ok" }));
    const result = await castVote("id-token-123", "hr-119-1", "yea");
    expect(result.status).toBe("ok");
    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain("/api/v1/bills/hr-119-1/vote");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ vote: "yea" });
    expect(opts.headers.Authorization).toBe("Bearer id-token-123");
  });
});

describe("deleteVote", () => {
  it("sends DELETE /bills/{id}/vote with the ID token and accepts the 204 (#593)", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 204,
      statusText: "No Content",
      json: () => Promise.reject(new SyntaxError("Unexpected end of JSON input")),
      text: () => Promise.resolve(""),
    });
    await expect(deleteVote("id-token-123", "hr-119-1")).resolves.toBeUndefined();
    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toMatch(/\/api\/v1\/bills\/hr-119-1\/vote$/);
    expect(opts.method).toBe("DELETE");
    expect(opts.body).toBeUndefined();
    expect(opts.headers.Authorization).toBe("Bearer id-token-123");
  });

  it("throws an ApiError when the API fails", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ error: "internal error" }, 500));
    await expect(deleteVote("id-token-123", "hr-119-1")).rejects.toBeInstanceOf(ApiError);
  });
});

describe("App Check on the vote and import calls (#122)", () => {
  const source = vi.fn(async (): Promise<string | null> => "app-check-token");

  beforeEach(() => {
    source.mockClear();
    setAppCheckSource(source);
    mockFetch.mockResolvedValue(mockResponse({ id: "u-1", state: "CA", district: 12, created_at: "x" }));
  });
  afterEach(() => setAppCheckSource(null));

  it("sends X-Firebase-AppCheck with a vote", async () => {
    await castVote("id-token-123", "hr-119-1", "yea");
    expect(mockFetch.mock.calls[0][1].headers["X-Firebase-AppCheck"]).toBe("app-check-token");
    expect(mockFetch.mock.calls[0][1].headers.Authorization).toBe("Bearer id-token-123");
  });

  it("doesn't ask for a token on the other signed-in calls", async () => {
    await createMe("id-token-123");
    await getMe("id-token-123");
    await updateMe("id-token-123", { state: "CA", district: 12 });
    await getMyVotes("id-token-123");
    // The API checks App Check only where votes are cast, not where they're removed.
    await deleteVote("id-token-123", "hr-119-1");
    expect(source).not.toHaveBeenCalled();
    for (const [, opts] of mockFetch.mock.calls) expect(opts.headers).not.toHaveProperty("X-Firebase-AppCheck");
  });

  it("still sends the vote, without the header, when there's no token or App Check fails", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    source.mockResolvedValueOnce(null).mockRejectedValueOnce(new Error("reCAPTCHA blocked"));
    await castVote("id-token-123", "hr-119-1", "yea");
    await castVote("id-token-123", "hr-119-1", "nay");
    expect(mockFetch).toHaveBeenCalledTimes(2);
    for (const [, opts] of mockFetch.mock.calls) expect(opts.headers).not.toHaveProperty("X-Firebase-AppCheck");
    expect(warn).toHaveBeenCalledTimes(1);
    warn.mockRestore();
  });
});

describe("getScorecard", () => {
  it("sends the ID token", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ scores: [] }));
    await getScorecard("id-token-123");
    const headers = mockFetch.mock.calls[0][1]?.headers as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer id-token-123");
  });
});

describe("account calls (#137)", () => {
  it("createMe POSTs /me with the token and no body", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ id: "u-1", created_at: "2026-09-29T00:00:00Z" }, 201));
    const result = await createMe("id-token-123");
    expect(result.id).toBe("u-1");
    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toMatch(/\/api\/v1\/me$/);
    expect(opts.method).toBe("POST");
    expect(opts.body).toBeUndefined();
    expect(opts.headers.Authorization).toBe("Bearer id-token-123");
  });

  it("getMe sends the token and returns the account", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ id: "u-1", state: "CA", district: 12, created_at: "2025-01-01" }));
    const result = await getMe("id-token-123");
    expect(result).toMatchObject({ id: "u-1", state: "CA", district: 12 });
    expect(mockFetch.mock.calls[0][1].headers.Authorization).toBe("Bearer id-token-123");
    expect(mockFetch.mock.calls[0][1].next).toBeUndefined();
  });

  it("updateMe PATCHes only the state and district", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ id: "u-1", state: "IL", district: 13, created_at: "2025-01-01" }));
    await updateMe("id-token-123", { state: "IL", district: 13 });
    const [, opts] = mockFetch.mock.calls[0];
    expect(opts.method).toBe("PATCH");
    expect(JSON.parse(opts.body as string)).toEqual({ state: "IL", district: 13 });
  });

  it("exposes the API's error code, e.g. no_account", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ error: "no account", code: "no_account" }, 403));
    const err = await getMe("id-token-123").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as InstanceType<typeof ApiError>).code).toBe("no_account");
  });

  it("exportMe GETs /me/export with the token, uncached", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ user: { id: "u-1" }, auth_uid: "a", votes: [], favorites: [] }));
    const data = await exportMe("id-token-123");
    expect(data.auth_uid).toBe("a");
    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toMatch(/\/api\/v1\/me\/export$/);
    expect(opts.headers.Authorization).toBe("Bearer id-token-123");
    expect(opts.next).toBeUndefined();
  });

  it("deleteMe DELETEs /me and accepts the 204 without a body", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 204,
      statusText: "No Content",
      json: () => Promise.reject(new SyntaxError("Unexpected end of JSON input")),
      text: () => Promise.resolve(""),
    });
    await expect(deleteMe("id-token-123")).resolves.toBeUndefined();
    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toMatch(/\/api\/v1\/me$/);
    expect(opts.method).toBe("DELETE");
    expect(opts.headers.Authorization).toBe("Bearer id-token-123");
  });

  it("deleteMe surfaces requires_recent_login", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ error: "sign in again", code: "requires_recent_login" }, 401));
    const err = await deleteMe("id-token-123").catch((e: unknown) => e);
    expect((err as InstanceType<typeof ApiError>).code).toBe("requires_recent_login");
  });

  it("importMyVotes POSTs the rows to /me/votes:import", async () => {
    mockFetch.mockResolvedValueOnce(mockResponse({ imported: 1, skipped: 0 }));
    const rows = [{ bill_id: "hr-119-1", vote: "yea" as const, voted_at: "2026-10-01T00:00:00.000Z" }];
    expect(await importMyVotes("id-token-123", rows)).toEqual({ imported: 1, skipped: 0 });
    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toMatch(/\/api\/v1\/me\/votes:import$/);
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ votes: rows });
  });

  it("has no code when the body isn't JSON or has none", () => {
    expect(new ApiError(502, "Bad Gateway", "<html>").code).toBeUndefined();
    expect(new ApiError(404, "Not Found", '{"error":"x"}').code).toBeUndefined();
  });
});

describe("error handling", () => {
  it("throws ApiError on non-200 response", async () => {
    mockFetch.mockResolvedValue({
      ok: false,
      status: 404,
      statusText: "Not Found",
      text: () => Promise.resolve('{"error":"bill not found"}'),
    });
    await expect(getBill("nonexistent")).rejects.toThrow(ApiError);
    try {
      await getBill("nonexistent");
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
      expect((e as InstanceType<typeof ApiError>).status).toBe(404);
    }
  });

  it("throws ApiError on 500", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: false,
      status: 500,
      statusText: "Internal Server Error",
      text: () => Promise.resolve('{"error":"internal error"}'),
    });
    await expect(listBills()).rejects.toThrow("API error 500");
  });
});

// The bill page's section reads (#872), each against the API route that serves it.
describe("bill section reads", () => {
  it.each([
    ["listTextVersions", () => listTextVersions("hr-119-1"), "/api/v1/bills/hr-119-1/text"],
    ["getBillText", () => getBillText("hr-119-1", "ih"), "/api/v1/bills/hr-119-1/text/ih"],
    ["listDiffs", () => listDiffs("hr-119-1"), "/api/v1/bills/hr-119-1/diffs"],
    ["getDiff", () => getDiff("hr-119-1", "d1"), "/api/v1/bills/hr-119-1/diffs/d1"],
    ["listAmendments", () => listAmendments("hr-119-1"), "/api/v1/bills/hr-119-1/amendments"],
    ["listGAOReports", () => listGAOReports("hr-119-1"), "/api/v1/bills/hr-119-1/gao-reports"],
  ])("%s GETs its route and returns the body", async (_name, call, path) => {
    mockFetch.mockResolvedValueOnce(mockResponse({ marker: path }));
    expect(await call()).toEqual({ marker: path });
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(new URL(url).pathname).toBe(path);
    expect(init.method).toBeUndefined();
  });
});

// What each failure turns into (#872): pages tell a 404 apart from everything else (isNotFound
// → notFound()), the vote store reads a 429's code, and a timeout is never mistaken for a 404.
describe("errors by status", () => {
  function failing(status: number, statusText: string, body: string) {
    return { ok: false, status, statusText, text: () => Promise.resolve(body), json: () => Promise.reject(new Error("no")) };
  }

  async function rejection(call: Promise<unknown>): Promise<unknown> {
    return call.then(
      () => {
        throw new Error("resolved");
      },
      (err: unknown) => err
    );
  }

  it.each([
    [404, "Not Found", '{"error":"bill not found"}', undefined, true],
    [429, "Too Many Requests", '{"error":"slow down","code":"daily_vote_cap"}', "daily_vote_cap", false],
    [500, "Internal Server Error", '{"error":"internal error"}', undefined, false],
  ])("turns a %i into an ApiError with its status, body and code", async (status, statusText, body, code, notFound) => {
    mockFetch.mockResolvedValue(failing(status, statusText, body));
    const err = await rejection(getMember("X000001"));
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status, statusText, body, message: `API error ${status}: ${statusText}` });
    expect((err as InstanceType<typeof ApiError>).code).toBe(code);
    expect(isNotFound(err)).toBe(notFound);
  });

  it("passes a timeout through as the fetch's own error, not an ApiError or a not-found", async () => {
    const timeout = new DOMException("The operation was aborted due to timeout", "TimeoutError");
    mockFetch.mockRejectedValue(timeout);
    const err = await rejection(getMember("X000001"));
    expect(err).toBe(timeout);
    expect(isNotFound(err)).toBe(false);
  });
});

describe("getRelatedBills", () => {
  it("calls /bills/{id}/related, with limit only when given", async () => {
    mockFetch.mockResolvedValue(mockResponse([]));
    await getRelatedBills("hr-119-1");
    await getRelatedBills("hr-119-1", 5);
    expect(mockFetch.mock.calls[0][0]).toMatch(/\/api\/v1\/bills\/hr-119-1\/related$/);
    expect(mockFetch.mock.calls[1][0]).toMatch(/\/api\/v1\/bills\/hr-119-1\/related\?limit=5$/);
  });
});

describe("getCompanionVotes", () => {
  it("calls /bills/{id}/companion-votes", async () => {
    mockFetch.mockResolvedValue(mockResponse([]));
    await expect(getCompanionVotes("hr-119-1")).resolves.toEqual([]);
    expect(mockFetch.mock.calls[0][0]).toMatch(/\/api\/v1\/bills\/hr-119-1\/companion-votes$/);
  });
});

describe("getBillLawChanges", () => {
  it("calls /bills/{id}/law-changes", async () => {
    mockFetch.mockResolvedValue(mockResponse({ bill_id: "hr-119-1", changes: [] }));
    await getBillLawChanges("hr-119-1");
    expect(mockFetch.mock.calls[0][0]).toMatch(/\/api\/v1\/bills\/hr-119-1\/law-changes$/);
  });
});

describe("getLawSection", () => {
  it("calls /law/{title}/{section}", async () => {
    mockFetch.mockResolvedValue(mockResponse({ section_id: "/us/usc/t42/s1395w-4" }));
    await getLawSection(42, "1395w-4");
    expect(mockFetch.mock.calls[0][0]).toMatch(/\/api\/v1\/law\/42\/1395w-4$/);
  });
});

describe("getMemberPositions", () => {
  it("calls /members/{id}/positions with the congress", async () => {
    mockFetch.mockResolvedValue(mockResponse({ member_id: "A000001", congress: 119, rule: "r", positions: [] }));
    const res = await getMemberPositions("A000001", 119);
    expect(res.congress).toBe(119);
    expect(mockFetch.mock.calls[0][0]).toMatch(/\/api\/v1\/members\/A000001\/positions\?congress=119$/);
  });
});

describe("getCollaborators", () => {
  it("calls /members/{id}/collaborators with congress and limit", async () => {
    mockFetch.mockResolvedValue(mockResponse({ congress: 118, collaborators: [] }));
    const res = await getCollaborators("A000001", { congress: 118 });
    expect(res.congress).toBe(118);
    expect(mockFetch.mock.calls[0][0]).toMatch(/\/api\/v1\/members\/A000001\/collaborators\?congress=118$/);
    await getCollaborators("A000001");
    expect(mockFetch.mock.calls[1][0]).toMatch(/\/collaborators$/);
  });
});

describe("getReps", () => {
  it("POSTs the address in the body, never the URL", async () => {
    mockFetch.mockResolvedValue(mockResponse({ reps: [], senators: [], districts: [] }));
    const address = "1100 Congress Ave, Austin, TX 78701";
    await getReps(address);
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toMatch(/\/api\/v1\/reps$/);
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ address });
    expect((init.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
  });
});

describe("getRepsAt", () => {
  it("POSTs the point in the body, never the URL (#711)", async () => {
    mockFetch.mockResolvedValue(mockResponse({ reps: [], senators: [], districts: [] }));
    await getRepsAt(38.8977, -77.0365);
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toMatch(/\/api\/v1\/reps$/);
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ lat: 38.8977, lon: -77.0365 });
  });
});

describe("the web server's key (#270)", () => {
  const key = "test-server-key-0123456789abcdefghijkl";

  afterEach(() => {
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
    vi.stubGlobal("fetch", mockFetch);
  });

  it("sends X-Server-Key from API_SERVER_KEY on server-side calls", async () => {
    vi.stubEnv("API_SERVER_KEY", key);
    mockFetch.mockResolvedValue(mockResponse({}));
    await getBill("119-hr-1");
    await getMe("id-token-123");
    for (const [, init] of mockFetch.mock.calls as [string, RequestInit][]) {
      const headers = init.headers as Record<string, string>;
      expect(headers["X-Server-Key"]).toBe(key);
      expect(headers["Content-Type"]).toBe("application/json");
    }
    expect((mockFetch.mock.calls[1][1].headers as Record<string, string>).Authorization).toBe("Bearer id-token-123");
  });

  it("sends no key when API_SERVER_KEY is unset, and the call still works", async () => {
    vi.stubEnv("API_SERVER_KEY", "");
    mockFetch.mockResolvedValue(mockResponse({ bill: { id: "119-hr-1" } }));
    const res = await getBill("119-hr-1");
    expect(res).toEqual({ bill: { id: "119-hr-1" } });
    const headers = mockFetch.mock.calls[0][1]?.headers as Record<string, string>;
    expect(headers).not.toHaveProperty("X-Server-Key");
  });

});

describe("a filtered list counted per visitor (#607)", () => {
  const key = "test-server-key-0123456789abcdefghijkl";
  const list = { items: [], total: 0, offset: 0, limit: 12 };

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("sends the visitor's IP with the server key on Vercel, and doesn't cache the call", async () => {
    vi.stubEnv("VERCEL", "1");
    vi.stubEnv("API_SERVER_KEY", key);
    mockFetch.mockResolvedValue(mockResponse(list));
    await listBills({ q: "tax" }, undefined, { visitor: true });
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toContain("/api/v1/bills?q=tax");
    expect(init.cache).toBe("no-store");
    expect(init.next).toBeUndefined();
    expect(init.headers).toMatchObject({ "X-Visitor-IP": "203.0.113.7", "X-Server-Key": key });
  });

  it("keeps the list cached and sends no IP off Vercel", async () => {
    vi.stubEnv("VERCEL", "");
    mockFetch.mockResolvedValue(mockResponse(list));
    await listBills({ q: "tax" }, undefined, { visitor: true });
    const init = mockFetch.mock.calls[0][1] as RequestInit;
    expect(init.next).toEqual({ revalidate: REVALIDATE.list, tags: [CACHE_TAGS.bills] });
    expect(init.cache).toBeUndefined();
    expect(init.headers).not.toHaveProperty("X-Visitor-IP");
  });

  it("sends no IP unless the caller opts in", async () => {
    vi.stubEnv("VERCEL", "1");
    mockFetch.mockResolvedValue(mockResponse(list));
    await listBills({ q: "tax" });
    await listBillsWithSummaries({ status: "became_law" });
    await getBill("hr-119-1");
    for (const [, init] of mockFetch.mock.calls as [string, RequestInit][]) {
      expect(init.headers).not.toHaveProperty("X-Visitor-IP");
      expect(init.next).toBeDefined();
    }
  });
});

describe("isPartialBillDetail (#454)", () => {
  const whole = {
    bill: { id: "hr-119-1" },
    actions: [],
    summary: null,
    text_versions: [],
    diffs: [],
    amendments: [],
    votes: [],
    status_history: [],
    gao_reports: [],
    sponsorships: [],
    crs_summary: null,
  } as unknown as Parameters<typeof isPartialBillDetail>[0];

  it("is false when every list is there, even empty, and the summaries are null (none)", () => {
    expect(isPartialBillDetail(whole)).toBe(false);
  });

  it("is false for an older response without gao_reports or sponsorships", () => {
    const older = { ...whole };
    delete older.gao_reports;
    delete older.sponsorships;
    expect(isPartialBillDetail(older)).toBe(false);
  });

  it.each(["actions", "text_versions", "diffs", "amendments", "votes", "status_history", "gao_reports", "sponsorships"])(
    "is true when %s is null",
    (section) => {
      expect(isPartialBillDetail({ ...whole, [section]: null })).toBe(true);
    },
  );
});
