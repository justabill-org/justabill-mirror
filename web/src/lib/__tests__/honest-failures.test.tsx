import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { isValidElement, type ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import type { Bill, BillCardFacts, BillListItem, Congress, PaginatedResult } from "../types";

// #73: pages show the API's data, a real 404 or the error page; never example data outside
// `next dev`. The API is mocked; vitest runs with NODE_ENV=test, so fixtures are off unless a
// test stubs NODE_ENV=development.

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  getBill: vi.fn(),
  getRelatedBills: vi.fn(),
  getScorecard: vi.fn(),
  listBills: vi.fn(),
  listBillsWithCards: vi.fn(),
  listBillsWithSummaries: vi.fn(),
  listCongresses: vi.fn(),
  listPolicyAreas: vi.fn(),
}));

const devUser = { value: undefined as string | undefined };
vi.mock("next/headers", () => ({
  cookies: async () => ({ get: () => (devUser.value ? { value: devUser.value } : undefined) }),
}));

const api = await import("../api");
const { ApiError } = api;
const examples = await import("../examples");
const { default: Home } = await import("@/app/page");
const { default: BillDetailPage } = await import("@/app/(app)/bills/[id]/page");
const { default: VotePage } = await import("@/app/(app)/vote/page");
const { default: ScorecardPage, metadata: scorecardMetadata } = await import("@/app/(app)/scorecard/page");
const { FilteredDeck } = await import("@/app/(app)/vote/filtered-deck");
const { LocalScorecard } = await import("@/components/scorecard/local-scorecard");

/** The first element of the given type in a server component's returned tree. */
function findElement<P>(node: unknown, type: (props: P) => unknown): ReactElement<P> | undefined {
  if (Array.isArray(node)) {
    for (const child of node) {
      const found = findElement(child, type);
      if (found) return found;
    }
    return undefined;
  }
  if (!isValidElement<{ children?: unknown }>(node)) return undefined;
  if (node.type === type) return node as unknown as ReactElement<P>;
  return findElement(node.props.children, type);
}

const congresses: Congress[] = [
  { number: 119, start_date: "2025-01-03", is_current: false },
  { number: 120, start_date: "2027-01-03", is_current: true },
];

function law(number: number, title: string): Bill {
  return {
    ...examples.billsBecameLaw.items[0],
    id: `hr-120-${number}`,
    congress: 120,
    bill_type: "hr",
    number,
    title,
  };
}

function page(items: Bill[]): PaginatedResult<Bill> {
  return { items, total: items.length, offset: 0, limit: items.length };
}

/** The same page as `include=summary` returns it: each bill with no summary yet. */
function withSummaries(bills: Bill[]): PaginatedResult<BillListItem> {
  const items = bills.map((b) => ({ ...b, summary: null }));
  return { items, total: items.length, offset: 0, limit: items.length };
}

const unavailable = () => new ApiError(503, "Service Unavailable", "");
const params = (id: string) => ({ params: Promise.resolve({ id }) });

beforeEach(() => {
  vi.mocked(api.getBill).mockReset();
  vi.mocked(api.getRelatedBills).mockReset().mockResolvedValue([]);
  vi.mocked(api.getScorecard).mockReset();
  vi.mocked(api.listBills).mockReset();
  vi.mocked(api.listBillsWithCards).mockReset();
  vi.mocked(api.listCongresses).mockReset().mockResolvedValue(congresses);
  vi.mocked(api.listPolicyAreas).mockReset().mockResolvedValue({ policy_areas: [] });
  devUser.value = undefined;
  vi.spyOn(console, "error").mockImplementation(() => {});
  vi.spyOn(console, "warn").mockImplementation(() => {});
});

afterEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

describe("home page", () => {
  // The counts row reads one limit=1 list per status; a mocked list of two bills counts as two.
  it("shows the current congress, its recent laws and its counts from the API", async () => {
    vi.mocked(api.listBills).mockResolvedValue(page([law(1, "First real law"), law(2, "Second real law")]));
    vi.mocked(api.listBillsWithSummaries).mockResolvedValue(
      withSummaries([law(1, "First real law"), law(2, "Second real law")]),
    );

    const html = renderToStaticMarkup(await Home());

    expect(html).toContain("The 120th Congress so far");
    expect(html).toContain("First real law");
    expect(html).toContain('href="/bills/hr-120-2"');
    expect(html).not.toContain(examples.billsBecameLaw.items[0].title);
    expect(api.listBillsWithSummaries).toHaveBeenCalledWith(
      expect.objectContaining({ congress: 120, status: "became_law", sort: "updated_at", limit: 4 })
    );
    expect(api.listBills).toHaveBeenCalledWith(expect.objectContaining({ congress: 120, limit: 1 }));
  });

  it("says when no bill has become law yet", async () => {
    vi.mocked(api.listBills).mockResolvedValue(page([]));
    vi.mocked(api.listBillsWithSummaries).mockResolvedValue(withSummaries([]));

    expect(renderToStaticMarkup(await Home())).toContain("No bills have become law in this Congress yet.");
  });

  it("still renders, without example bills, counts or a congress, when the API is down", async () => {
    vi.mocked(api.listCongresses).mockRejectedValue(unavailable());
    vi.mocked(api.listBills).mockRejectedValue(new TypeError("fetch failed"));
    vi.mocked(api.listBillsWithSummaries).mockRejectedValue(new TypeError("fetch failed"));

    const html = renderToStaticMarkup(await Home());

    expect(html).toContain("Recent laws aren&#x27;t available right now.");
    expect(html).not.toContain("Congress so far");
    expect(html).not.toContain(examples.billsBecameLaw.items[0].title);
    expect(html).toContain("What Congress is doing, in plain language.");
  });

  it("lists laws from every congress when only the congress list fails", async () => {
    vi.mocked(api.listCongresses).mockRejectedValue(unavailable());
    vi.mocked(api.listBills).mockResolvedValue(page([law(4, "Law without a congress filter")]));
    vi.mocked(api.listBillsWithSummaries).mockResolvedValue(withSummaries([law(4, "Law without a congress filter")]));

    const html = renderToStaticMarkup(await Home());

    expect(html).toContain("All laws of the Congress");
    expect(html).toContain("Law without a congress filter");
    expect(api.listBillsWithSummaries).toHaveBeenCalledWith(expect.objectContaining({ congress: undefined }));
  });

  it("keeps the congress name and the counts when only the law list fails", async () => {
    vi.mocked(api.listBills).mockResolvedValue(page([law(1, "Counted")]));
    vi.mocked(api.listBillsWithSummaries).mockRejectedValue(unavailable());

    const html = renderToStaticMarkup(await Home());

    expect(html).toContain("All laws of the 120th Congress");
    expect(html).toContain("The 120th Congress so far");
    expect(html).toContain("Recent laws aren&#x27;t available right now.");
  });

  it("uses the fixtures under next dev", async () => {
    vi.stubEnv("NODE_ENV", "development");
    vi.mocked(api.listCongresses).mockRejectedValue(new TypeError("fetch failed"));
    vi.mocked(api.listBills).mockRejectedValue(new TypeError("fetch failed"));
    vi.mocked(api.listBillsWithSummaries).mockRejectedValue(new TypeError("fetch failed"));

    const html = renderToStaticMarkup(await Home());

    expect(html).toContain("The 119th Congress so far");
    expect(html).toContain(examples.billsBecameLaw.items[0].title);
  });
});

describe("bill page", () => {
  it("is a 404 for an unknown bill", async () => {
    vi.mocked(api.getBill).mockRejectedValue(new ApiError(404, "Not Found", ""));

    await expect(BillDetailPage(params("hr-119-999999"))).rejects.toMatchObject({
      digest: "NEXT_HTTP_ERROR_FALLBACK;404",
    });
  });

  it("is a 404 for an unknown bill under next dev too, not the example bill", async () => {
    vi.stubEnv("NODE_ENV", "development");
    vi.mocked(api.getBill).mockRejectedValue(new ApiError(400, "Bad Request", ""));

    await expect(BillDetailPage(params("not-a-bill"))).rejects.toMatchObject({
      digest: "NEXT_HTTP_ERROR_FALLBACK;404",
    });
  });

  it("throws to the error page when the API fails", async () => {
    const err = unavailable();
    vi.mocked(api.getBill).mockRejectedValue(err);

    await expect(BillDetailPage(params("hr-119-1"))).rejects.toBe(err);
  });

  it("renders the bill from the API", async () => {
    vi.mocked(api.getBill).mockResolvedValue({
      ...examples.billDetail,
      bill: { ...examples.billDetail.bill, id: "hr-120-7", title: "A real bill from the API" },
    });

    const html = renderToStaticMarkup(await BillDetailPage(params("hr-120-7")));

    expect(html).toContain("A real bill from the API");
  });

  it("shows the example bill when the API is down under next dev", async () => {
    vi.stubEnv("NODE_ENV", "development");
    vi.mocked(api.getBill).mockRejectedValue(new TypeError("fetch failed"));

    const html = renderToStaticMarkup(await BillDetailPage(params("hr-119-1")));

    expect(html).toContain(examples.billDetail.bill.title);
  });

  // Until go-public the API is private and answers a preview's calls with a bare 404 (#678).
  it("shows the example bill on a Vercel preview when the private API answers 404", async () => {
    vi.stubEnv("VERCEL_ENV", "preview");
    vi.spyOn(console, "warn").mockImplementation(() => {});
    vi.mocked(api.getBill).mockRejectedValue(new ApiError(404, "Not Found", ""));

    const html = renderToStaticMarkup(await BillDetailPage(params("hr-119-1")));

    expect(html).toContain(examples.billDetail.bill.title);
  });

  it("is a 404 for an unknown bill in Vercel production, never the example bill", async () => {
    vi.stubEnv("VERCEL_ENV", "production");
    vi.mocked(api.getBill).mockRejectedValue(new ApiError(404, "Not Found", ""));

    await expect(BillDetailPage(params("hr-119-999999"))).rejects.toMatchObject({
      digest: "NEXT_HTTP_ERROR_FALLBACK;404",
    });
  });
});

describe("vote page", () => {
  const card: BillCardFacts = {
    crs: null,
    passage: [{ chamber: "House", method: "voice", date: "2026-01-05T00:00:00Z", result: "Passed" }],
    enacted: { date: "2026-02-01T00:00:00Z", law_type: "public", law_number: "120-3" },
    law_change_count: 0,
  };

  it("renders the first batch of Laws in the current congress, not a hard-coded one (#797)", async () => {
    const first = { ...page([]), items: [{ ...law(3, "Law to vote on"), summary: null, card }], total: 1 };
    vi.mocked(api.listBillsWithCards).mockResolvedValue(first);

    const tree = await VotePage();

    // One list for the default deck: the Laws view's statuses, newest action first, a batch of 20.
    expect(vi.mocked(api.listBillsWithCards).mock.calls).toEqual([
      [{ congress: 120, sort: "latest_action", status: ["became_law", "signed"], offset: 0, limit: 20 }],
    ]);
    // The deck is dealt in the browser (#215), so check what the page hands it.
    const deck = findElement(tree, FilteredDeck);
    expect(deck?.props.first).toBe(first);
    expect(deck?.props.congresses).toEqual(congresses);
  });

  it("hands the filter every policy area, or null when they can't be read (#708)", async () => {
    vi.mocked(api.listBillsWithCards).mockResolvedValue({ ...page([]), items: [], total: 0 });
    vi.mocked(api.listPolicyAreas).mockResolvedValue({ policy_areas: ["Health", "Taxation"] });
    expect(findElement(await VotePage(), FilteredDeck)?.props.policyAreas).toEqual(["Health", "Taxation"]);

    vi.mocked(api.listPolicyAreas).mockRejectedValue(unavailable());
    expect(findElement(await VotePage(), FilteredDeck)?.props.policyAreas).toBeNull();
  });

  it("throws to the error page when the bill list fails", async () => {
    const err = unavailable();
    vi.mocked(api.listBillsWithCards).mockRejectedValue(err);

    await expect(VotePage()).rejects.toBe(err);
  });

  it("prerenders an unavailable state, not example bills, when the build can't reach the API", async () => {
    vi.stubEnv("NEXT_PHASE", "phase-production-build");
    vi.mocked(api.listCongresses).mockRejectedValue(new TypeError("fetch failed"));

    const html = renderToStaticMarkup(await VotePage());

    expect(html).toContain("available right now");
    expect(html).not.toContain(examples.billList.items[0].title);
  });

  it("throws to the error page when the congress list fails", async () => {
    const err = unavailable();
    vi.mocked(api.listCongresses).mockRejectedValue(err);

    await expect(VotePage()).rejects.toBe(err);
    expect(api.listBillsWithCards).not.toHaveBeenCalled();
  });
});

describe("scorecard page", () => {
  // The scorecard is built in the browser from this device's votes (#215): the server page never
  // fetches one, so it has no example scorecard to fall back to.
  it("never fetches a scorecard on the server", async () => {
    devUser.value = "user-1";

    renderToStaticMarkup(await ScorecardPage());

    expect(api.getScorecard).not.toHaveBeenCalled();
  });

  it("is called Scorecard, the menu's name for it, in its heading and title (#764)", async () => {
    const html = renderToStaticMarkup(await ScorecardPage());

    expect(html).toMatch(/<h1[^>]*>Scorecard<\/h1>/);
    expect(scorecardMetadata.title).toBe("Scorecard | Just a Bill");
  });

  it("offers the congresses with votes loaded on its switch (#243)", async () => {
    vi.mocked(api.listCongresses).mockResolvedValue([
      { ...congresses[0], has_votes: true },
      { ...congresses[1], has_votes: true },
      { number: 121, start_date: "2029-01-03", is_current: false, has_votes: false },
    ]);

    const scorecard = findElement(await ScorecardPage(), LocalScorecard);

    expect(scorecard?.props.offered).toEqual([120, 119]);
  });

  it("prerenders without a switch when the build can't reach the API", async () => {
    vi.stubEnv("NEXT_PHASE", "phase-production-build");
    vi.mocked(api.listCongresses).mockRejectedValue(new TypeError("fetch failed"));

    const scorecard = findElement(await ScorecardPage(), LocalScorecard);

    expect(scorecard?.props.offered).toEqual([]);
  });

  it("throws to the error page when the congress list fails", async () => {
    const err = unavailable();
    vi.mocked(api.listCongresses).mockRejectedValue(err);

    await expect(ScorecardPage()).rejects.toBe(err);
  });
});
