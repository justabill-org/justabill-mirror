import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { BillDetailResponse, BillLawChangesResponse } from "../types";

// #316: the bill page renders the law-changes panel, and renders without it when the call fails.

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

const detail: BillDetailResponse = {
  ...examples.billDetail,
  bill: { ...examples.billDetail.bill, id: "hr-119-7" },
  text_versions: [
    { id: "v", bill_id: "hr-119-7", version_type: "Reported in House", version_code: "rh", date: "2025-09-10",
      formats: [], sort_order: 1 },
  ],
};

const changes: BillLawChangesResponse = {
  bill_id: "hr-119-7",
  version_id: "v",
  version_code: "rh",
  explained: null,
  ai_generated: false,
  current_release_point: { release_point: "119-111", source_url: "" },
  changes: [
    {
      section_id: "/us/usc/t7/s2012",
      in_us_code: true,
      loaded: true,
      title_number: 7,
      section_number: "2012",
      heading: "Definitions",
      change_kind: "amends",
      cite_text: null,
      subsection_path: null,
      instruction: null,
      explanation: null,
      also_changed_by: [],
    },
  ],
};

async function renderBill(): Promise<string> {
  return renderToStaticMarkup(await BillDetailPage({ params: Promise.resolve({ id: "hr-119-7" }) }));
}

beforeEach(() => {
  vi.mocked(api.getBill).mockReset().mockResolvedValue(detail);
  vi.mocked(api.getRelatedBills).mockReset().mockResolvedValue([]);
  vi.mocked(api.getCompanionVotes).mockReset().mockResolvedValue([]);
  vi.mocked(api.getBillLawChanges).mockReset();
});

describe("bill page law changes", () => {
  it("shows the panel, naming the text version it describes", async () => {
    vi.mocked(api.getBillLawChanges).mockResolvedValue(changes);
    const html = await renderBill();
    expect(api.getBillLawChanges).toHaveBeenCalledWith("hr-119-7");
    expect(html).toContain("Changes to current law");
    expect(html).toContain("<em>Reported in House</em> text");
    expect(html).toContain("7 U.S.C. 2012");
  });

  it("renders the page without the panel when the call fails", async () => {
    vi.mocked(api.getBillLawChanges).mockRejectedValue(new Error("down"));
    const html = await renderBill();
    expect(html).toContain(detail.bill.title.replace(/'/g, "&#x27;"));
    expect(html).not.toContain("Changes to current law");
  });
});
