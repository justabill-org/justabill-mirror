import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { BillDetailResponse, CrsSummary } from "../types";

// #423: the CRS summary card on the bill page, and the JSON-LD abstract.

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  getBill: vi.fn(),
  getRelatedBills: vi.fn(),
  getCompanionVotes: vi.fn(),
}));

const api = await import("../api");
const examples = await import("../examples");
const { default: BillDetailPage } = await import("@/app/(app)/bills/[id]/page");

const crs: CrsSummary = {
  bill_id: "s-119-5",
  version_code: "00",
  action_date: "2025-06-05T00:00:00Z",
  action_desc: "Introduced in Senate",
  text: "This bill does a thing.\n\n• First item",
  updated_at: "2026-09-28T12:47:20Z",
};

const detail = (over: Partial<BillDetailResponse>): BillDetailResponse => ({
  ...examples.billDetail,
  bill: { ...examples.billDetail.bill, id: "s-119-5", congress: 119, bill_type: "s", number: 5 },
  text_versions: [],
  ...over,
});

async function renderBill(d: BillDetailResponse): Promise<string> {
  vi.mocked(api.getBill).mockResolvedValue(d);
  return renderToStaticMarkup(await BillDetailPage({ params: Promise.resolve({ id: "s-119-5" }) }));
}

/** The page's JSON-LD object. */
function jsonLd(html: string): Record<string, unknown> {
  const m = /<script type="application\/ld\+json">(.*?)<\/script>/.exec(html);
  if (!m) throw new Error("no JSON-LD");
  return JSON.parse(m[1]) as Record<string, unknown>;
}

beforeEach(() => {
  vi.mocked(api.getBill).mockReset();
  vi.mocked(api.getRelatedBills).mockReset().mockResolvedValue([]);
  vi.mocked(api.getCompanionVotes).mockReset().mockResolvedValue([]);
});

describe("bill page CRS summary", () => {
  it("shows the official summary under the AI summary", async () => {
    const html = await renderBill(detail({ crs_summary: crs, summary: { bill_id: "s-119-5", short_summary: "AI" } }));
    expect(html).toContain("Official summary");
    expect(html.indexOf("AI Summary")).toBeLessThan(html.indexOf("Official summary"));
    expect(html).toContain('href="https://www.congress.gov/bill/119th-congress/senate-bill/5/summary/00"');
    expect(html).not.toContain("changed since");
  });

  it("shows only the CRS summary, with no empty AI card, without an AI summary (#664)", async () => {
    const html = await renderBill(detail({ crs_summary: crs, summary: null }));
    expect(html).not.toContain("No AI summary available");
    expect(html).toContain("Official summary");
  });

  it("says the bill changed when a text version is newer than the summary", async () => {
    const html = await renderBill(
      detail({
        crs_summary: crs,
        text_versions: [
          { id: "v", bill_id: "s-119-5", version_type: "Engrossed", version_code: "es", date: "2025-09-10",
            formats: [], sort_order: 1 },
        ],
      }),
    );
    expect(html).toContain("The bill has changed since this summary was written.");
  });

  it("sets the JSON-LD abstract to the CRS summary's first paragraph, never the AI summary", async () => {
    const html = await renderBill(
      detail({ crs_summary: crs, summary: { bill_id: "s-119-5", short_summary: "AI words" } }),
    );
    expect(jsonLd(html).abstract).toBe("This bill does a thing.");
  });

  it("shows no card and no abstract without a CRS summary", async () => {
    for (const crsSummary of [null, undefined]) {
      const html = await renderBill(detail({ crs_summary: crsSummary, summary: { bill_id: "s-119-5", short_summary: "AI" } }));
      expect(html).not.toContain("Official summary");
      expect(jsonLd(html)).not.toHaveProperty("abstract");
    }
  });
});
