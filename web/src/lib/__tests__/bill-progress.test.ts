import { describe, expect, it } from "vitest";
import { billProgress, lifecycle } from "../bill-progress";
import { billDetail } from "../examples";
import type { BillStatusEntry } from "../types";

// #664: the bill page's progress steps, with dates, skipped and unrecorded steps, and the current one.

const entry = (status: BillStatusEntry["status"], date: string): BillStatusEntry => ({
  status,
  status_date: `${date}T00:00:00Z`,
  status_rank: 0,
});

describe("lifecycle", () => {
  it("starts with the chamber the bill was introduced in", () => {
    expect(lifecycle("hr", false)).toEqual([
      "introduced", "in_committee", "reported", "passed_house", "passed_senate", "resolving_differences",
      "to_president", "signed", "became_law",
    ]);
    expect(lifecycle("s", false).slice(3, 5)).toEqual(["passed_senate", "passed_house"]);
    expect(lifecycle("sjres", false).slice(3, 5)).toEqual(["passed_senate", "passed_house"]);
    expect(lifecycle("hjres", false).slice(3, 5)).toEqual(["passed_house", "passed_senate"]);
  });

  it("keeps a simple resolution in its chamber and a concurrent one away from the President", () => {
    expect(lifecycle("hres", false)).toEqual(["introduced", "in_committee", "reported", "passed_house"]);
    expect(lifecycle("sres", false)).toEqual(["introduced", "in_committee", "reported", "passed_senate"]);
    expect(lifecycle("hconres", false).at(-1)).toBe("resolving_differences");
    expect(lifecycle("sconres", false)).not.toContain("to_president");
  });

  it("shows the veto in place of the signature, with Became law still ahead", () => {
    const steps = lifecycle("hr", true);
    expect(steps).toContain("vetoed");
    expect(steps).not.toContain("signed");
    expect(steps.at(-1)).toBe("became_law");
  });
});

describe("billProgress", () => {
  it("gives H.R. 187's reached steps their dates, and names the skipped and unrecorded ones", () => {
    const { bill, status_history } = billDetail;
    const steps = billProgress({
      billType: bill.bill_type,
      currentStatus: bill.current_status,
      statusHistory: status_history ?? [],
      introducedDate: bill.introduced_date,
    });
    expect(steps.map((s) => [s.status, s.state, s.date?.slice(0, 10)])).toEqual([
      // Introduced has no history entry: the bill's introduced date stands in.
      ["introduced", "reached", "2025-01-03"],
      ["in_committee", "reached", "2025-01-22"],
      // The committee was discharged: no report.
      ["reported", "skipped", undefined],
      // A bill can't become law without passing the House: the record is missing the date (#659).
      ["passed_house", "unrecorded", undefined],
      ["passed_senate", "reached", "2025-12-16"],
      ["resolving_differences", "skipped", undefined],
      ["to_president", "reached", "2025-12-18"],
      ["signed", "reached", "2025-12-26"],
      ["became_law", "current", "2025-12-26"],
    ]);
  });

  it("marks the steps after the current one as upcoming, without a date", () => {
    const steps = billProgress({
      billType: "s",
      currentStatus: "in_committee",
      statusHistory: [entry("introduced", "2026-03-02"), entry("in_committee", "2026-03-02")],
    });
    expect(steps.find((s) => s.state === "current")?.status).toBe("in_committee");
    expect(steps.slice(2).every((s) => s.state === "upcoming" && s.date === undefined)).toBe(true);
  });

  it("dates a step by the first time the bill reached it", () => {
    const steps = billProgress({
      billType: "hr",
      currentStatus: "in_committee",
      statusHistory: [entry("in_committee", "2026-05-01"), entry("in_committee", "2026-02-10")],
      introducedDate: "2026-02-09T00:00:00Z",
    });
    expect(steps[1].date).toBe("2026-02-10T00:00:00Z");
  });

  it("says an original measure began in committee, not that its referral date is missing (#781)", () => {
    // hres-119-1530's history in production: a Rules Committee rule, reported as an original
    // measure, so it was never referred and has no in_committee or introduced row.
    const steps = billProgress({
      billType: "hres",
      currentStatus: "passed_house",
      statusHistory: [entry("reported", "2026-09-14"), entry("passed_house", "2026-09-15")],
      introducedDate: "2026-09-14T00:00:00Z",
    });
    expect(steps.map((s) => [s.status, s.state, s.date?.slice(0, 10)])).toEqual([
      ["introduced", "reached", "2026-09-14"],
      ["in_committee", "originated", undefined],
      ["reported", "reached", "2026-09-14"],
      ["passed_house", "current", "2026-09-15"],
    ]);
  });

  it("keeps a referral's date when the bill was also reported", () => {
    const steps = billProgress({
      billType: "hr",
      currentStatus: "reported",
      statusHistory: [entry("in_committee", "2026-01-06"), entry("reported", "2026-02-01")],
      introducedDate: "2026-01-05T00:00:00Z",
    });
    expect(steps[1]).toEqual({ status: "in_committee", state: "reached", date: "2026-01-06T00:00:00Z" });
  });

  it("still says a passage's date isn't recorded when a law has no passed_house row", () => {
    const steps = billProgress({
      billType: "hr",
      currentStatus: "became_law",
      statusHistory: [
        entry("introduced", "2026-01-05"), entry("in_committee", "2026-01-05"), entry("passed_senate", "2026-03-01"),
        entry("to_president", "2026-03-05"), entry("signed", "2026-03-10"), entry("became_law", "2026-03-10"),
      ],
    });
    expect(steps.find((s) => s.status === "passed_house")).toEqual({ status: "passed_house", state: "unrecorded" });
    expect(steps.find((s) => s.status === "reported")?.state).toBe("skipped");
  });

  it("doesn't invent an introduced date when the bill has neither the row nor the date", () => {
    const steps = billProgress({
      billType: "hres",
      currentStatus: "passed_house",
      statusHistory: [entry("reported", "2026-09-14"), entry("passed_house", "2026-09-15")],
    });
    expect(steps[0]).toEqual({ status: "introduced", state: "unrecorded" });
  });

  it("gives each of s-119-2's steps its own date, in lifecycle order, though the House passed it last", () => {
    // s-119-2's history in production: an original measure, no introduced row, and the House
    // passage dated after the Senate's.
    const steps = billProgress({
      billType: "s",
      currentStatus: "became_law",
      statusHistory: [
        entry("reported", "2026-05-20"), entry("passed_house", "2026-06-09"), entry("passed_senate", "2026-06-05"),
        entry("to_president", "2026-06-09"), entry("signed", "2026-06-10"), entry("became_law", "2026-06-10"),
      ],
      introducedDate: "2026-05-20T00:00:00Z",
    });
    expect(steps.map((s) => [s.status, s.state, s.date?.slice(0, 10)])).toEqual([
      ["introduced", "reached", "2026-05-20"],
      ["in_committee", "originated", undefined],
      ["reported", "reached", "2026-05-20"],
      ["passed_senate", "reached", "2026-06-05"],
      ["passed_house", "reached", "2026-06-09"],
      ["resolving_differences", "skipped", undefined],
      ["to_president", "reached", "2026-06-09"],
      ["signed", "reached", "2026-06-10"],
      ["became_law", "current", "2026-06-10"],
    ]);
  });

  it("calls a committee skipped when the bill passed without one", () => {
    const steps = billProgress({
      billType: "s",
      currentStatus: "passed_senate",
      statusHistory: [entry("introduced", "2026-01-05"), entry("passed_senate", "2026-01-05")],
    });
    expect(steps.slice(1, 3).map((s) => s.state)).toEqual(["skipped", "skipped"]);
    expect(steps[3]).toMatchObject({ status: "passed_senate", state: "current", date: "2026-01-05T00:00:00Z" });
  });

  it("follows a vetoed bill to the veto, with Became law still ahead", () => {
    const steps = billProgress({
      billType: "hr",
      currentStatus: "vetoed",
      statusHistory: [entry("to_president", "2026-04-01"), entry("vetoed", "2026-04-10")],
    });
    expect(steps.slice(-3).map((s) => [s.status, s.state])).toEqual([
      ["to_president", "reached"],
      ["vetoed", "current"],
      ["became_law", "upcoming"],
    ]);
  });

  it("has no current step when the bill's status isn't on its lifecycle", () => {
    const steps = billProgress({ billType: "hr", statusHistory: [entry("introduced", "2026-01-05")] });
    expect(steps.some((s) => s.state === "current")).toBe(false);
    expect(steps[0].state).toBe("reached");
  });
});
