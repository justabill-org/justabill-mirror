import { describe, expect, it, vi } from "vitest";
import { loadBillStatuses, type BillStatusesApi } from "@/lib/bill-statuses";
import type { BillStatusesResponse, Congress } from "@/lib/types";

// Where the bills stand for My votes (#843): every congress's GET /bill-statuses (#853), merged.

const CONGRESSES: Congress[] = [
  { number: 119, start_date: "2025-01-03T00:00:00Z", end_date: "2027-01-03T00:00:00Z", is_current: true, has_votes: true },
  { number: 118, start_date: "2023-01-03T00:00:00Z", end_date: "2025-01-03T00:00:00Z", is_current: false, has_votes: true },
];

const LISTS: Record<number, BillStatusesResponse> = {
  119: { congress: 119, bills: [{ id: "hr-119-1", status: "became_law" }, { id: "s-119-5", status: "passed_senate" }] },
  118: { congress: 118, bills: [{ id: "hr-118-9", status: "vetoed" }] },
};

function fakeApi(failing: number[] = []): BillStatusesApi & { getBillStatuses: ReturnType<typeof vi.fn> } {
  return {
    listCongresses: vi.fn(async () => CONGRESSES),
    getBillStatuses: vi.fn(async (n: number) => {
      if (failing.includes(n)) throw new Error(`congress ${n} failed`);
      return LISTS[n];
    }),
  };
}

describe("loadBillStatuses", () => {
  it("reads the list of every congress the site has and merges them", async () => {
    const api = fakeApi();
    const statuses = await loadBillStatuses(api);

    expect(api.getBillStatuses.mock.calls.map(([n]) => n).sort()).toEqual([118, 119]);
    expect(statuses.byId).toEqual({ "hr-119-1": "became_law", "s-119-5": "passed_senate", "hr-118-9": "vetoed" });
    expect([...statuses.congresses].sort()).toEqual([118, 119]);
  });

  it("leaves out a congress whose list fails, so its bills stay unknown, and reports the error", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const statuses = await loadBillStatuses(fakeApi([118]));

    expect(statuses.byId).toEqual({ "hr-119-1": "became_law", "s-119-5": "passed_senate" });
    expect([...statuses.congresses]).toEqual([119]);
    expect(error).toHaveBeenCalledWith(new Error("congress 118 failed"));
    error.mockRestore();
  });

  it("knows no status when the congress list fails, and never rejects", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const api = { ...fakeApi(), listCongresses: vi.fn(async () => Promise.reject(new Error("offline"))) };
    const statuses = await loadBillStatuses(api);

    expect(statuses.byId).toEqual({});
    expect(statuses.congresses.size).toBe(0);
    expect(api.getBillStatuses).not.toHaveBeenCalled();
    error.mockRestore();
  });
});
