// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import type { BillDetailResponse, DisapprovedRule } from "../types";
import { axeViolations } from "@/test/axe";

// #643: a CRA resolution's page shows the rule it disapproves, directly under the AI summary and
// above the CRS summary (docs/design/590-cra-disapproved-rules.md, "Web").

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  getBill: vi.fn(),
  getRelatedBills: vi.fn(),
  getCompanionVotes: vi.fn(),
  getBillLawChanges: vi.fn(),
  getBillAggregates: vi.fn(),
}));

const api = await import("../api");
const examples = await import("../examples");
const { default: BillDetailPage } = await import("@/app/(app)/bills/[id]/page");

const CARD = /^The rule this resolution disapproves/;

// The dev fixture is S.J.Res. 18 as the production API served it, with its rule.
const sjres18 = examples.billDetailCra;
const rule = sjres18.disapproved_rule as DisapprovedRule;

async function renderBill(d: BillDetailResponse) {
  vi.mocked(api.getBill).mockResolvedValue(d);
  return render(await BillDetailPage({ params: Promise.resolve({ id: d.bill.id }) }));
}

/** The page's card headings, in order. */
const headings = () => screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent ?? "");

beforeEach(() => {
  vi.mocked(api.getBill).mockReset();
  vi.mocked(api.getRelatedBills).mockReset().mockResolvedValue([]);
  vi.mocked(api.getCompanionVotes).mockReset().mockResolvedValue([]);
  // The page hides both panels when their reads fail.
  vi.mocked(api.getBillLawChanges).mockReset().mockRejectedValue(new Error("no law changes"));
  vi.mocked(api.getBillAggregates).mockReset().mockRejectedValue(new Error("no aggregates"));
});

afterEach(cleanup);

describe("bill page, a CRA resolution's disapproved rule", () => {
  it("shows S.J.Res. 18's rule, with its links, in the Summary section", async () => {
    await renderBill(sjres18);
    const section = screen.getByRole("region", { name: "Summary" });
    within(section).getByRole("heading", { name: CARD });
    expect(within(section).getByRole("link", { name: "Official PDF (GovInfo)" }).getAttribute("href")).toBe(
      "https://www.govinfo.gov/content/pkg/FR-2024-12-30/pdf/2024-29699.pdf",
    );
    expect(within(section).getByRole("link", { name: "Docket CFPB-2024-0002 on Regulations.gov" })).toBeTruthy();
  });

  it("puts the card under the AI summary and above the CRS summary", async () => {
    await renderBill(sjres18);
    const order = headings();
    const ai = order.findIndex((h) => h.startsWith("AI Summary"));
    const card = order.findIndex((h) => CARD.test(h));
    expect(ai).toBeGreaterThanOrEqual(0);
    expect(card).toBe(ai + 1);
    const crs = screen.getByText("Official summary from the Congressional Research Service");
    const cardHeading = screen.getByRole("heading", { name: CARD });
    expect(cardHeading.compareDocumentPosition(crs) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("shows the card with neither summary, next to the vote", async () => {
    await renderBill({ ...sjres18, summary: null, crs_summary: null });
    const section = screen.getByRole("region", { name: "Summary" });
    within(section).getByRole("heading", { name: CARD });
    expect(within(section).queryByRole("heading", { name: /AI Summary/ })).toBeNull();
  });

  it("shows no card without a rule, or on a bill that isn't a joint resolution", async () => {
    for (const d of [
      { ...sjres18, disapproved_rule: null },
      { ...sjres18, disapproved_rule: undefined },
      { ...sjres18, bill: { ...sjres18.bill, bill_type: "s" as const } },
    ]) {
      await renderBill(d);
      expect(screen.queryByRole("heading", { name: CARD })).toBeNull();
      cleanup();
    }
  });

  it("shows an unmatched rule as unmatched, with no document", async () => {
    await renderBill({
      ...sjres18,
      disapproved_rule: { ...rule, status: "unmatched", method: undefined, reason: "ambiguous", document: null },
    });
    expect(screen.getByText(/couldn't match this resolution to a Federal Register document/)).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Official PDF (GovInfo)" })).toBeNull();
  });

  it("names the Federal Register in the AI disclaimer when the summary used the rule", async () => {
    await renderBill({ ...sjres18, summary: { ...sjres18.summary!, with_crs_summary: false, with_rule_context: true } });
    const disclaimer = screen.getAllByRole("note").find((n) => n.textContent?.startsWith("AI-generated"));
    expect(disclaimer?.textContent).toContain(
      "with the Federal Register's description of the rule as context",
    );
  });

  it("has no axe violations with the card", async () => {
    const { container } = await renderBill(sjres18);
    expect(await axeViolations(container)).toEqual([]);
  });

  it("has no axe violations with an unmatched rule", async () => {
    const { container } = await renderBill({
      ...sjres18,
      disapproved_rule: { ...rule, status: "unmatched", reason: "no_candidates", gao_opinion: true, document: null },
    });
    expect(await axeViolations(container)).toEqual([]);
  });
});
