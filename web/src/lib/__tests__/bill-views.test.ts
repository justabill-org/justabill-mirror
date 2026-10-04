import { describe, expect, it } from "vitest";
import {
  BILL_STEPS,
  BILL_VIEWS,
  billDescription,
  billStep,
  firstSentence,
  parseBillSort,
  parseBillView,
  viewCounts,
  viewStatuses,
} from "../bill-views";
import { billsBecameLaw } from "../examples";
import type { BillListItem, BillStatus, CardCrs } from "../types";

// The quick views, sort and card helpers on /bills (#666).

const base = billsBecameLaw.items[0];

describe("views", () => {
  it("lists Laws, Passed a chamber, In committee and All, in that order", () => {
    expect(BILL_VIEWS.map((v) => v.label)).toEqual(["Laws", "Passed a chamber", "In committee", "All"]);
  });

  it("puts each status in at most one view, so the counts add up", () => {
    const statuses = BILL_VIEWS.flatMap((v) => v.statuses);
    expect(new Set(statuses).size).toBe(statuses.length);
  });

  it.each([
    [undefined, "laws"],
    ["laws", "laws"],
    ["passed", "passed"],
    ["committee", "committee"],
    ["all", "all"],
    ["nonsense", "laws"],
  ])("reads ?show=%s as %s", (raw, key) => {
    expect(parseBillView(raw).key).toBe(key);
  });

  it.each([
    [undefined, "latest_action"],
    ["latest_action", "latest_action"],
    ["updated_at", "latest_action"],
    ["introduced_date", "introduced_date"],
    ["number", "latest_action"],
  ])("reads ?sort=%s as %s", (raw, sort) => {
    expect(parseBillSort(raw)).toBe(sort);
  });
});

describe("viewStatuses and viewCounts (#712, #713)", () => {
  it("lists each view's statuses in one filter, and none for All", () => {
    expect(viewStatuses(parseBillView("laws"))).toEqual(["became_law", "signed"]);
    expect(viewStatuses(parseBillView("passed"))).toHaveLength(5);
    expect(viewStatuses(parseBillView("committee"))).toEqual(["in_committee"]);
    expect(viewStatuses(parseBillView("all"))).toBeUndefined();
  });

  it("sums each view's statuses from one counts answer, and takes the total for All", () => {
    const counts = viewCounts({
      by_status: { became_law: 40, signed: 1, passed_house: 7, vetoed: 2, in_committee: 300, introduced: 50 },
      total: 405,
    });
    expect(counts).toEqual({ laws: 41, passed: 9, committee: 300, all: 405 });
  });

  it("counts a view with no bills as zero", () => {
    expect(viewCounts({ by_status: {}, total: 0 })).toEqual({ laws: 0, passed: 0, committee: 0, all: 0 });
  });
});

describe("billStep", () => {
  it.each<[BillStatus | undefined, string]>([
    [undefined, "Introduced"],
    ["introduced", "Introduced"],
    ["in_committee", "Introduced"],
    ["reported", "Introduced"],
    ["passed_house", "Passed one chamber"],
    ["passed_senate", "Passed one chamber"],
    ["resolving_differences", "Passed both chambers"],
    ["to_president", "Passed both chambers"],
    ["vetoed", "Passed both chambers"],
    ["signed", "Became law"],
    ["became_law", "Became law"],
  ])("puts %s at %s", (status, step) => {
    expect(BILL_STEPS[billStep(status) - 1]).toBe(step);
  });
});

describe("firstSentence", () => {
  it.each([
    ["This bill funds parks. It also names a post office.", "This bill funds parks."],
    ["It amends the U.S. Code to add a fee. Then more.", "It amends the U.S. Code to add a fee."],
    ["It amends H.R. 1 and Sec. 4 of the Act. More.", "It amends H.R. 1 and Sec. 4 of the Act."],
    ["Starting Jan. 1, 2027, it applies. Later.", "Starting Jan. 1, 2027, it applies."],
    ["It names John Q. Public as director. Then.", "It names John Q. Public as director."],
    ["One sentence with no end", "One sentence with no end"],
    ["  Spaced\n  out.   Next.", "Spaced out."],
    ["Is it a question? Yes.", "Is it a question?"],
  ])("cuts %j to %j", (text, want) => {
    expect(firstSentence(text)).toBe(want);
  });
});

describe("billDescription", () => {
  const crs = (lead: string): CardCrs => ({ version_code: "00", action_date: "2025-01-03", action_desc: "Introduced", lead });
  const item = (short_summary: string | undefined, crsSummary?: CardCrs | null): BillListItem => ({
    ...base,
    summary: short_summary === undefined ? null : ({ short_summary } as BillListItem["summary"]),
    crs_summary: crsSummary,
  });

  it("is the AI summary's first sentence, labeled as AI", () => {
    expect(billDescription(item("Funds parks. Names a post office."))).toEqual({
      label: "AI summary",
      text: "Funds parks.",
    });
  });

  it("prefers the AI summary to the CRS summary", () => {
    expect(billDescription(item("Funds parks.", crs("This bill funds parks."))!)).toMatchObject({ label: "AI summary" });
  });

  it("falls back to the CRS summary's first sentence, labeled as CRS (#714)", () => {
    const got = billDescription(item(undefined, crs("This bill requires the U.S. Postal Service to act. It also funds.")));
    expect(got).toEqual({ label: "CRS summary", text: "This bill requires the U.S. Postal Service to act." });
    expect(billDescription(item("  ", crs("This bill funds parks.")))).toEqual({
      label: "CRS summary",
      text: "This bill funds parks.",
    });
  });

  it("is null with neither summary, or with empty ones", () => {
    expect(billDescription(item(undefined))).toBeNull();
    expect(billDescription(item(undefined, null))).toBeNull();
    expect(billDescription(item("  ", crs(" ")))).toBeNull();
  });
});
