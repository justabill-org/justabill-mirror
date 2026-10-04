import { describe, expect, it } from "vitest";
import { mergeActions, OVERVIEW_SOURCE } from "../bill-actions";
import { billDetail } from "../examples";
import type { BillAction } from "../types";

// #664: the action timeline merges the same action from several sources and marks the key ones.

let next = 0;
function action(over: Partial<BillAction> & Pick<BillAction, "action_date" | "action_text">): BillAction {
  next += 1;
  return { id: `a${next}`, bill_id: "hr-119-1", sort_order: next, ...over };
}

describe("mergeActions", () => {
  it("merges the same day and text from two sources into one entry with both sources", () => {
    const merged = mergeActions([
      action({ action_date: "2026-03-01T00:00:00Z", action_text: "Presented to President.", source_system: "House floor actions" }),
      action({ action_date: "2026-03-01T00:00:00Z", action_text: "Presented to  President. ", source_system: OVERVIEW_SOURCE }),
    ]);
    expect(merged).toHaveLength(1);
    expect(merged[0]).toMatchObject({
      date: "2026-03-01T00:00:00Z",
      text: "Presented to President.",
      sources: ["House floor actions", OVERVIEW_SOURCE],
      isKey: true,
    });
  });

  it("lists a source once when it recorded the same action twice", () => {
    const twice = { action_date: "2026-03-01T00:00:00Z", action_text: "Signed by President.", source_system: OVERVIEW_SOURCE };
    const merged = mergeActions([action(twice), action(twice)]);
    expect(merged).toHaveLength(1);
    expect(merged[0].sources).toEqual([OVERVIEW_SOURCE]);
  });

  it("keeps the same text on different days, and different text on the same day, apart", () => {
    const merged = mergeActions([
      action({ action_date: "2026-03-01T00:00:00Z", action_text: "Considered." }),
      action({ action_date: "2026-03-02T00:00:00Z", action_text: "Considered." }),
      action({ action_date: "2026-03-02T00:00:00Z", action_text: "Passed." }),
    ]);
    expect(merged.map((a) => [a.date.slice(0, 10), a.text])).toEqual([
      ["2026-03-02", "Considered."],
      ["2026-03-02", "Passed."],
      ["2026-03-01", "Considered."],
    ]);
  });

  it("puts the newest first, and same-day actions in their recorded order", () => {
    const merged = mergeActions([
      action({ action_date: "2026-01-01T00:00:00Z", action_text: "Introduced." }),
      action({ action_date: "2026-02-01T00:00:00Z", action_text: "Second", sort_order: 2 }),
      action({ action_date: "2026-02-01T00:00:00Z", action_text: "First", sort_order: 1 }),
    ]);
    expect(merged.map((a) => a.text)).toEqual(["First", "Second", "Introduced."]);
  });

  it("calls an action key when Congress.gov's overview lists it or it has a roll-call vote", () => {
    const vote = { roll_number: 12, url: "https://clerk.house.gov/Votes/202612", chamber: "House" };
    const merged = mergeActions([
      action({ action_date: "2026-03-03T00:00:00Z", action_text: "On passage Passed.", recorded_vote: vote }),
      action({ action_date: "2026-03-02T00:00:00Z", action_text: "Reported.", source_system: OVERVIEW_SOURCE }),
      action({ action_date: "2026-03-01T00:00:00Z", action_text: "Debate.", source_system: "House floor actions" }),
    ]);
    expect(merged.map((a) => a.isKey)).toEqual([true, true, false]);
    expect(merged[0].recordedVote).toEqual(vote);
    expect(merged[0].sources).toEqual([]);
  });

  it("keeps the roll-call vote from whichever source recorded it", () => {
    const vote = { roll_number: 7, url: "https://clerk.house.gov/Votes/20267", chamber: "House" };
    const merged = mergeActions([
      action({ action_date: "2026-03-03T00:00:00Z", action_text: "Passed.", source_system: OVERVIEW_SOURCE }),
      action({ action_date: "2026-03-03T00:00:00Z", action_text: "Passed.", source_system: "House floor actions",
        recorded_vote: vote }),
    ]);
    expect(merged).toHaveLength(1);
    expect(merged[0].recordedVote).toEqual(vote);
  });

  it("folds H.R. 187's 20 actions from three sources into 16, 6 of them key", () => {
    const merged = mergeActions(billDetail.actions ?? []);
    expect(merged).toHaveLength(16);
    expect(merged.filter((a) => a.isKey)).toHaveLength(6);
    expect(merged[0]).toMatchObject({ text: "Became Public Law No: 119-62.", sources: [OVERVIEW_SOURCE] });
    expect(merged.find((a) => a.text === "Presented to President.")?.sources).toEqual([
      "House floor actions",
      OVERVIEW_SOURCE,
    ]);
  });

  it("returns nothing for no actions", () => {
    expect(mergeActions([])).toEqual([]);
  });
});
