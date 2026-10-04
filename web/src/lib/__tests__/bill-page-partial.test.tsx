import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { BillDetailResponse } from "../types";

// #454: the API answers 200 with a section null when that section's read failed. The page renders
// the rest and says which part is missing; getBill keeps it from being cached as the page.

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  getBill: vi.fn(),
  getRelatedBills: vi.fn(),
  getCompanionVotes: vi.fn(),
  getBillLawChanges: vi.fn(),
}));

const api = await import("../api");
const examples = await import("../examples");
const { default: BillDetailPage } = await import("@/app/(app)/bills/[id]/page");

async function renderBill(d: BillDetailResponse): Promise<string> {
  vi.mocked(api.getBill).mockResolvedValue(d);
  return renderToStaticMarkup(await BillDetailPage({ params: Promise.resolve({ id: d.bill.id }) }));
}

beforeEach(() => {
  vi.mocked(api.getBill).mockReset();
  vi.mocked(api.getRelatedBills).mockReset().mockResolvedValue([]);
  vi.mocked(api.getCompanionVotes).mockReset().mockResolvedValue([]);
  vi.mocked(api.getBillLawChanges).mockReset().mockRejectedValue(new Error("not under test"));
});

describe("bill page with a partial detail", () => {
  it("renders every other section when each list section is null", async () => {
    const html = await renderBill({
      ...examples.billDetail,
      actions: null,
      text_versions: null,
      diffs: null,
      amendments: null,
      votes: null,
      status_history: null,
      gao_reports: null,
      sponsorships: null,
    });
    expect(html).toContain(examples.billDetail.bill.title);
    expect(html).toContain("Legislative progress");
    // Only the active tab's panel is rendered: Actions.
    expect(html).toContain("The actions couldn&#x27;t be loaded right now.");
    // Tabs without a count rather than "(0)", which would claim there are none.
    for (const tab of ["Actions", "Text", "Votes", "Amendments", "GAO reports"]) {
      expect(html).toMatch(new RegExp(`>${tab}</button>`));
    }
  });

  it("keeps the label the production uptime check matches (#790)", async () => {
    // infra's "Web bill page" uptime check (stacks/observability, web_uptime_bill) looks for this text on
    // /bills/hr-119-1. Renaming it files a false critical "Web down" alert: change the check in step with it.
    const html = await renderBill(examples.billDetail);
    expect(html).toContain('aria-label="Bill status progress"');
  });

  it("keeps the counts and content of the sections that did load", async () => {
    const actions = examples.billDetail.actions ?? [];
    const html = await renderBill({ ...examples.billDetail, votes: null });
    expect(html).toContain(`Actions (${actions.length})`);
    expect(html).toContain(`Text (${(examples.billDetail.text_versions ?? []).length})`);
    expect(html).toMatch(/>Votes<\/button>/);
    expect(html).toContain("Action timeline");
    expect(html).not.toContain("couldn&#x27;t be loaded");
  });

  it("treats a missing gao_reports (an older API) as none, not as failed", async () => {
    const older = { ...examples.billDetail };
    delete older.gao_reports;
    const html = await renderBill(older);
    expect(html).not.toContain("GAO reports");
    expect(html).not.toContain("couldn&#x27;t be loaded");
  });

  it("leaves out the tabs with nothing in them, and keeps one that didn't load (#717)", async () => {
    const html = await renderBill({ ...examples.billDetail, amendments: [], gao_reports: [], votes: null });
    expect(html).not.toContain("Amendments");
    expect(html).not.toContain("GAO reports");
    expect(html).toMatch(/>Votes<\/button>/);
    expect(html).toMatch(/>Sponsors<\/button>/);
  });

  // The Text tab also lists the other chamber's companion bills, so a bill with no text yet keeps it
  // for them (the e2e suite caught HR 1's companions vanishing with its tab).
  it("keeps the Text tab of a bill with no text when it has a companion in the other chamber (#717)", async () => {
    const bill = examples.billDetail.bill;
    vi.mocked(api.getRelatedBills).mockResolvedValue([
      {
        bill_id: `s-${bill.congress}-1`,
        congress: bill.congress,
        bill_type: bill.bill_type.startsWith("s") ? "hr" : "s",
        number: 1,
        title: "A companion",
        relation_types: ["Identical bill"],
        shared_subjects: 0,
      },
    ]);
    expect(await renderBill({ ...examples.billDetail, text_versions: [] })).toMatch(/>Text \(0\)<\/button>/);

    vi.mocked(api.getRelatedBills).mockResolvedValue([]);
    expect(await renderBill({ ...examples.billDetail, bill: { ...bill, related_bills: [] }, text_versions: [] })).not.toMatch(/>Text( \(\d+\))?<\/button>/);
  });

  // The plan's Review Focus 2 (#872): every section degrades on its own. The /vote card's side is in
  // vote-card.test.tsx ("says so when there's no summary at all").
  it("renders a bill with no CRS summary, no AI summary and no text without their cards or tab (#872)", async () => {
    const bill = { ...examples.billDetail.bill, related_bills: [] };
    const html = await renderBill({
      ...examples.billDetail,
      bill,
      summary: null,
      crs_summary: null,
      text_versions: [],
      diffs: [],
    });
    expect(html).not.toContain('aria-label="Summary"');
    expect(html).not.toContain("AI Summary");
    expect(html).not.toContain("Official summary");
    expect(html).toContain('<div class="lg:col-span-5">');
    expect(html).not.toMatch(/>Text( \(\d+\))?<\/button>/);
    expect(html).toContain(`>${bill.title}</h1>`);
    expect(html).toContain('aria-label="Bill status progress"');
    expect(html).toMatch(/>Actions \(\d+\)<\/button>/);
    expect(html).toMatch(/>Sponsors<\/button>/);
  });
});
