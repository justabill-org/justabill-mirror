import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

// #634: a made-up bill URL costs at most one API call. A malformed ID is a 404 with none, a
// well-formed ID the API doesn't have costs only getBill, and a real bill's optional panels start
// together after getBill.

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  getBill: vi.fn(),
  getRelatedBills: vi.fn(),
  getCompanionVotes: vi.fn(),
  getBillLawChanges: vi.fn(),
  getBillAggregates: vi.fn(),
}));

const api = await import("../api");
const { ApiError } = api;
const examples = await import("../examples");
const { default: BillDetailPage, generateMetadata } = await import("@/app/(app)/bills/[id]/page");

const NOT_FOUND = { digest: "NEXT_HTTP_ERROR_FALLBACK;404" };

const optional = [api.getRelatedBills, api.getCompanionVotes, api.getBillLawChanges, api.getBillAggregates];

function params(id: string) {
  return { params: Promise.resolve({ id }) };
}

function apiCalls(): number {
  return [api.getBill, ...optional].reduce((n, fn) => n + vi.mocked(fn).mock.calls.length, 0);
}

beforeEach(() => {
  vi.mocked(api.getBill).mockReset();
  vi.mocked(api.getRelatedBills).mockReset().mockResolvedValue([]);
  vi.mocked(api.getCompanionVotes).mockReset().mockResolvedValue([]);
  vi.mocked(api.getBillLawChanges).mockReset().mockRejectedValue(new ApiError(404, "Not Found", ""));
  vi.mocked(api.getBillAggregates).mockReset().mockRejectedValue(new ApiError(404, "Not Found", ""));
});

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("bill page API calls", () => {
  it.each([
    "not-a-bill",
    "hr-119",
    "xx-119-1",
    "HR-119-1",
    "hr-119-01",
    "hr-119-1.php",
    "constructor-119-1",
    "hr-119-123456",
  ])("is a 404 with no API call for the malformed ID %s", async (id) => {
    await expect(BillDetailPage(params(id))).rejects.toMatchObject(NOT_FOUND);
    expect(apiCalls()).toBe(0);
  });

  it("is a 404 with no API call for a malformed ID under next dev too", async () => {
    vi.stubEnv("NODE_ENV", "development");

    await expect(BillDetailPage(params("not-a-bill"))).rejects.toMatchObject(NOT_FOUND);
    expect(apiCalls()).toBe(0);
  });

  it("gives a malformed ID's metadata without an API call", async () => {
    const metadata = await generateMetadata(params("not-a-bill"));

    expect(metadata.robots).toMatchObject({ index: false });
    expect(apiCalls()).toBe(0);
  });

  it("makes only the getBill call for a well-formed ID the API doesn't have", async () => {
    vi.mocked(api.getBill).mockRejectedValue(new ApiError(404, "Not Found", ""));

    await expect(BillDetailPage(params("hr-119-99999"))).rejects.toMatchObject(NOT_FOUND);
    expect(vi.mocked(api.getBill).mock.calls).toEqual([["hr-119-99999"]]);
    expect(apiCalls()).toBe(1);
  });

  it("makes no optional call when getBill fails", async () => {
    const err = new ApiError(503, "Service Unavailable", "");
    vi.mocked(api.getBill).mockRejectedValue(err);

    await expect(BillDetailPage(params("hr-119-1"))).rejects.toBe(err);
    expect(apiCalls()).toBe(1);
  });

  it("starts the four optional calls together once the bill has loaded", async () => {
    const id = examples.billDetail.bill.id;
    const order: string[] = [];
    vi.mocked(api.getBill).mockImplementation(async () => {
      order.push("getBill");
      return examples.billDetail;
    });
    // Each optional call waits until all four have started, so the render finishes only if they
    // run in parallel rather than one after another.
    let started = 0;
    let release!: () => void;
    const allStarted = new Promise<void>((resolve) => {
      release = resolve;
    });
    const parallel = (name: string) => async () => {
      order.push(name);
      if (++started === optional.length) release();
      await allStarted;
      throw new ApiError(404, "Not Found", "");
    };
    vi.mocked(api.getRelatedBills).mockImplementation(parallel("getRelatedBills"));
    vi.mocked(api.getCompanionVotes).mockImplementation(parallel("getCompanionVotes"));
    vi.mocked(api.getBillLawChanges).mockImplementation(parallel("getBillLawChanges"));
    vi.mocked(api.getBillAggregates).mockImplementation(parallel("getBillAggregates"));

    const html = renderToStaticMarkup(await BillDetailPage(params(id)));

    expect(html).toContain(examples.billDetail.bill.title);
    expect(order[0]).toBe("getBill");
    expect(order.slice(1).sort()).toEqual(
      ["getBillAggregates", "getBillLawChanges", "getCompanionVotes", "getRelatedBills"].sort()
    );
    for (const fn of optional) expect(vi.mocked(fn).mock.calls).toEqual([[id]]);
  });
});
