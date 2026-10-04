import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { BillDetailResponse } from "../types";

// #664: the bill page's progress and action cards, and its one Summary section.

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  getBill: vi.fn(),
  getRelatedBills: vi.fn(),
  getCompanionVotes: vi.fn(),
}));

const api = await import("../api");
const examples = await import("../examples");
const { default: BillDetailPage } = await import("@/app/(app)/bills/[id]/page");

const crs = {
  bill_id: "hr-119-187",
  version_code: "00",
  action_date: "2025-01-03T00:00:00Z",
  action_desc: "Introduced in House",
  text: "This bill does a thing.",
  updated_at: "2026-09-28T12:47:20Z",
};

async function renderBill(over: Partial<BillDetailResponse> = {}): Promise<string> {
  vi.mocked(api.getBill).mockResolvedValue({ ...examples.billDetail, ...over });
  return renderToStaticMarkup(await BillDetailPage({ params: Promise.resolve({ id: "hr-119-187" }) }));
}

/** The number of times a string occurs in the page. */
const count = (html: string, s: string) => html.split(s).length - 1;

beforeEach(() => {
  vi.mocked(api.getBill).mockReset();
  vi.mocked(api.getRelatedBills).mockReset().mockResolvedValue([]);
  vi.mocked(api.getCompanionVotes).mockReset().mockResolvedValue([]);
});

describe("bill page progress and actions (#664)", () => {
  it("shows the progress with dates, and the current step on its folded line", async () => {
    const html = await renderBill();
    expect(html).toContain("Legislative progress");
    expect(html).toContain("Became law, Dec 26, 2025 (step 9 of 9)");
    expect(html).toContain('aria-current="step"');
    expect(html).toContain('<time dateTime="2025-12-16">Dec 16, 2025</time>');
    expect(html).toContain("Skipped");
    expect(html).toContain("Date not recorded");
  });

  it("names the newest action on the Action timeline's folded line, and merges the sources", async () => {
    const html = await renderBill();
    expect(html).toContain("Latest, Dec 26, 2025: Became Public Law No: 119-62.");
    // Recorded twice by Congress.gov's overview and once each elsewhere: one entry.
    expect(count(html, "Presented to President.")).toBe(1);
    expect(html).toContain("Show all 16 actions");
  });

  it("folds the progress and action cards on phones, behind buttons that say their state", async () => {
    const html = await renderBill();
    expect(count(html, 'aria-expanded="false"')).toBeGreaterThanOrEqual(2);
    expect(html).toContain("max-lg:hidden");
  });
});

describe("bill page summary (#664)", () => {
  it("shows the AI summary with the CRS summary folded under it", async () => {
    const html = await renderBill({ summary: { bill_id: "hr-119-187", short_summary: "AI words" }, crs_summary: crs });
    expect(count(html, 'aria-label="Summary"')).toBe(1);
    expect(html).toContain("AI words");
    expect(html).toContain("<details");
    expect(html).toContain("Official summary from the Congressional Research Service");
  });

  it("shows the CRS summary alone when there's no AI summary, with no empty card", async () => {
    const html = await renderBill({ summary: null, crs_summary: crs });
    expect(html).toContain('aria-label="Summary"');
    expect(html).toContain("Congressional Research Service");
    expect(html).not.toContain("<details");
    expect(html).not.toContain("No AI summary available");
  });

  it("shows no summary section with neither, and gives the vote card the full width", async () => {
    const html = await renderBill({ summary: null, crs_summary: null });
    expect(html).not.toContain('aria-label="Summary"');
    expect(html).not.toContain("No AI summary available");
    expect(html).toContain('class="lg:col-span-5"');
  });
});
