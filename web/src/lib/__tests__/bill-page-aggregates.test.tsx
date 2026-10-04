import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { BillAggregatesResponse, BillDetailResponse } from "../types";

// #126: the bill page shows "How Just a Bill users voted" when the API serves aggregates, and
// leaves it out when the feature is off (404 aggregates_off) or the call fails.

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

const detail: BillDetailResponse = { ...examples.billDetail, bill: { ...examples.billDetail.bill, id: "hr-119-7" } };

const aggregates: BillAggregatesResponse = {
  bill_id: "hr-119-7",
  as_of: "2026-10-01T12:00:00Z",
  national: {
    scope: "national",
    scope_key: "",
    status: "published",
    yea_pct: 64,
    nay_pct: 36,
    voters_floor: 1200,
    published_at: "2026-10-01T12:00:00Z",
  },
  states: [],
  districts: [],
};

async function renderBill(): Promise<string> {
  return renderToStaticMarkup(await BillDetailPage({ params: Promise.resolve({ id: "hr-119-7" }) }));
}

beforeEach(() => {
  vi.mocked(api.getBill).mockReset().mockResolvedValue(detail);
  vi.mocked(api.getRelatedBills).mockReset().mockResolvedValue([]);
  vi.mocked(api.getCompanionVotes).mockReset().mockResolvedValue([]);
  vi.mocked(api.getBillLawChanges).mockReset().mockRejectedValue(new Error("none"));
  vi.mocked(api.getBillAggregates).mockReset();
});

describe("bill page aggregates", () => {
  it("shows the panel with the national cell", async () => {
    vi.mocked(api.getBillAggregates).mockResolvedValue(aggregates);
    const html = await renderBill();
    expect(api.getBillAggregates).toHaveBeenCalledWith("hr-119-7");
    expect(html).toContain("How Just a Bill users voted");
    expect(html).toContain("64%");
    expect(html).toContain("1,200+ users");
  });

  it("leaves the panel out while aggregates are off", async () => {
    vi.mocked(api.getBillAggregates).mockRejectedValue(
      new api.ApiError(404, "Not Found", '{"code":"aggregates_off","error":"aggregates aren\'t public"}'),
    );
    const html = await renderBill();
    expect(html).toContain(detail.bill.title);
    expect(html).not.toContain("How Just a Bill users voted");
  });
});
