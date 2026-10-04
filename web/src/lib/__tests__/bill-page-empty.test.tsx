import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { Bill, BillDetailResponse } from "../types";

// Review focus (v0.6.0..main): a bill with no CRS summary, no AI summary and no text opens its
// page, and every section degrades to its empty state.

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  getBill: vi.fn(),
  getRelatedBills: vi.fn(),
  getCompanionVotes: vi.fn(),
  getBillLawChanges: vi.fn(),
  getBillAggregates: vi.fn(),
}));

const api = await import("../api");
const page = await import("@/app/(app)/bills/[id]/page");
const BillDetailPage = page.default;

function bareBill(id: string, type: Bill["bill_type"], number: number): BillDetailResponse {
  return {
    bill: {
      id,
      congress: 119,
      bill_type: type,
      number,
      title: "A bill with nothing yet",
      introduced_date: "2025-06-05T00:00:00Z",
      current_status: "introduced",
    } as Bill,
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
    disapproved_rule: null,
  };
}

async function render(d: BillDetailResponse): Promise<string> {
  vi.mocked(api.getBill).mockResolvedValue(d);
  return renderToStaticMarkup(await BillDetailPage({ params: Promise.resolve({ id: d.bill.id }) }));
}

beforeEach(() => {
  vi.mocked(api.getBill).mockReset();
  vi.mocked(api.getRelatedBills).mockReset().mockResolvedValue([]);
  vi.mocked(api.getCompanionVotes).mockReset().mockResolvedValue([]);
  vi.mocked(api.getBillLawChanges).mockReset().mockRejectedValue(new Error("404"));
  vi.mocked(api.getBillAggregates).mockReset().mockRejectedValue(new Error("404"));
});

describe("bill page with no CRS summary, no AI summary and no text", () => {
  for (const [id, type, n] of [["hr-119-77", "hr", 77], ["hjres-119-9", "hjres", 9]] as const) {
    it(`renders every section's empty state (${type})`, async () => {
      const html = await render(bareBill(id, type, n));
      expect(html).toContain("A bill with nothing yet");
      // No summary section, and the vote card spans the row.
      expect(html).not.toContain('aria-label="Summary"');
      expect(html).toContain("lg:col-span-5");
      expect(html).not.toContain("The rule this resolution disapproves");
      // Progress still renders.
      expect(html).toContain('aria-label="Bill status progress"');
      // Tabs: only Actions and Sponsors; no Text tab, no "couldn't be loaded".
      expect(html).toContain("Actions (0)");
      expect(html).toContain("No actions recorded yet.");
      expect(html).not.toMatch(/>Text( \(0\))?</);
      expect(html).not.toContain("couldn&#x27;t be loaded");
      expect(html).toContain("Sponsors");
      // JSON-LD has no abstract.
      const ld = /<script type="application\/ld\+json">(.*?)<\/script>/.exec(html);
      expect(ld).not.toBeNull();
      expect(JSON.parse(ld![1])).not.toHaveProperty("abstract");
    });
  }

  it("the Text panel (shown for a companion) says there is no text yet", async () => {
    const { BillTextPanel } = await import("@/components/bill/bill-text-panel");
    const html = renderToStaticMarkup(
      <BillTextPanel billId="hr-119-77" billType="hr" versions={[]} diffs={[]} actions={[]} related={[
        { bill_id: "s-119-5", congress: 119, bill_type: "s", number: 5, title: "Companion",
          relation_types: ["Identical bill"], shared_subjects: 0 },
      ]} />,
    );
    expect(html).toContain("No text versions available yet.");
  });

  it("falls back to a generated description in the metadata", async () => {
    vi.mocked(api.getBill).mockResolvedValue(bareBill("hr-119-77", "hr", 77));
    const meta = await page.generateMetadata({ params: Promise.resolve({ id: "hr-119-77" }) });
    expect(meta.description).toBe("H.R. 77 in the 119th Congress. Read it in plain language and vote on it.");
  });

  it("an absent (older API) crs_summary/disapproved_rule and null actions still render", async () => {
    const d = bareBill("hr-119-77", "hr", 77) as Partial<BillDetailResponse>;
    delete d.crs_summary;
    delete d.disapproved_rule;
    delete d.gao_reports;
    const html = await render({ ...(d as BillDetailResponse), actions: null });
    expect(html).toContain("The actions couldn&#x27;t be loaded right now.");
    expect(html).not.toContain('aria-label="Summary"');
  });
});
