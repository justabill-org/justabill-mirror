import { existsSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

import {
  dayStart,
  EXPERIMENTS,
  experimentForPath,
  experimentsNow,
  findExperiment,
  isLive,
  matcherFor,
  registryProblems,
  treatmentPathname,
  type Experiment,
} from "../experiments/registry";

const OK: Experiment = {
  id: "vote-deck-layout",
  issue: 695,
  path: "/vote",
  hypothesis: "Both arms are the same page, so nothing should change.",
  goal: "casts a first vote on /vote",
  baseline: 0.1,
  target: 0.12,
  samplePerArm: 3841,
  start: "2026-10-12",
  end: "2026-10-26",
};

function problems(overrides: Partial<Experiment>, others: Experiment[] = []): string[] {
  return registryProblems([...others, { ...OK, ...overrides }]);
}

describe("the live registry", () => {
  it("keeps every rule", () => {
    expect(registryProblems(EXPERIMENTS)).toEqual([]);
  });

  // src/proxy.ts exists only while an experiment does, and matches exactly the experiments' pages, so
  // an end PR that removes the entry but forgets the proxy (or the reverse) fails here.
  it("has src/proxy.ts exactly when it has experiments, matching their paths", async () => {
    const proxyFile = path.resolve(__dirname, "../../proxy.ts");
    expect(existsSync(proxyFile)).toBe(EXPERIMENTS.length > 0);
    if (EXPERIMENTS.length > 0) {
      const proxy = (await import(/* @vite-ignore */ proxyFile)) as { config?: { matcher?: unknown } };
      expect(proxy.config?.matcher).toEqual(EXPERIMENTS.map((e) => matcherFor(e.path)));
    }
  });
});

describe("registryProblems", () => {
  it("accepts a two-week experiment with enough visitors", () => {
    expect(problems({})).toEqual([]);
  });

  it("refuses a window overlapping election week", () => {
    expect(problems({ start: "2026-10-26", end: "2026-11-09" })).toEqual([
      "vote-deck-layout: it overlaps election week (2026-11-02 to 11-06) without electionWeekApproved",
    ]);
    // Touching either edge isn't overlapping: it ends as the week starts, or starts the Saturday after.
    expect(problems({ start: "2026-10-19", end: "2026-11-02" })).toEqual([]);
    expect(problems({ start: "2026-11-07", end: "2026-11-14" })).toEqual([]);
  });

  it("allows election week only with the maintainer's flag", () => {
    expect(problems({ start: "2026-10-26", end: "2026-11-09", electionWeekApproved: true })).toEqual([]);
  });

  it("refuses more than 30 days", () => {
    expect(problems({ start: "2026-10-05", end: "2026-11-09", electionWeekApproved: true })).toEqual([
      "vote-deck-layout: it runs 35 days, more than 30",
    ]);
  });

  it("refuses a window that isn't whole weeks", () => {
    expect(problems({ end: "2026-10-22" })).toEqual(["vote-deck-layout: it runs 10 days, not whole weeks"]);
  });

  it("refuses an end before the start, and dates that aren't dates", () => {
    expect(problems({ end: "2026-10-12" })).toContain("vote-deck-layout: it must end after it starts");
    expect(problems({ start: "2026-02-30" })).toEqual(["vote-deck-layout: start and end must be YYYY-MM-DD dates"]);
    expect(problems({ end: "Oct 26" })).toEqual(["vote-deck-layout: start and end must be YYYY-MM-DD dates"]);
  });

  it("refuses too small a sample", () => {
    expect(problems({ samplePerArm: 3500 })).toEqual([
      "vote-deck-layout: samplePerArm 3500 is below the 3841 that 0.1 → 0.12 needs",
    ]);
  });

  it("refuses impossible rates", () => {
    expect(problems({ baseline: 0, target: 0.1 })).toEqual([
      "vote-deck-layout: sampleSizePerArm: baseline must be in (0, 1), got 0",
    ]);
  });

  it("refuses two experiments on one page, or at once", () => {
    const other: Experiment = { ...OK, id: "vote-other" };
    expect(problems({}, [other])).toEqual([
      "vote-deck-layout: vote-other already tests /vote",
      "vote-deck-layout: its window overlaps vote-other's (one experiment at a time)",
    ]);
    const later: Experiment = { ...OK, id: "bills-later", path: "/bills/[id]", start: "2026-10-26", end: "2026-11-02" };
    expect(problems({}, [later])).toEqual([]);
    expect(problems({ path: "/bills" }, [{ ...OK, id: "vote-deck-layout" }])).toContain(
      "vote-deck-layout: two experiments share this id"
    );
  });

  it("refuses ids, issues, paths and texts it can't use", () => {
    expect(problems({ id: "Vote.Deck" })).toEqual(["Vote.Deck: its id must be lowercase words joined by hyphens"]);
    expect(problems({ issue: 0 })).toEqual(["vote-deck-layout: it must name its issue"]);
    expect(problems({ path: "/" })).toEqual([]);
    expect(problems({ path: "/vote/v/treatment" })).toEqual([
      'vote-deck-layout: its path "/vote/v/treatment" isn\'t a page route',
    ]);
    expect(problems({ path: "/v/treatment" })).toHaveLength(1);
    expect(problems({ path: "vote?x=1" })).toHaveLength(1);
    expect(problems({ goal: " " })).toEqual(["vote-deck-layout: it needs a hypothesis and a goal"]);
  });
});

describe("windows and paths", () => {
  it("is live from 00:00 UTC on start until 00:00 UTC on end", () => {
    expect(isLive(OK, new Date("2026-10-11T23:59:59Z"))).toBe(false);
    expect(isLive(OK, new Date("2026-10-12T00:00:00Z"))).toBe(true);
    expect(isLive(OK, new Date("2026-10-25T23:59:59Z"))).toBe(true);
    expect(isLive(OK, new Date("2026-10-26T00:00:00Z"))).toBe(false);
  });

  it("takes the time from JAB_EXPERIMENTS_NOW only outside production", () => {
    const pinned = "2026-10-15T12:00:00Z";
    expect(experimentsNow({ JAB_EXPERIMENTS_NOW: pinned }).toISOString()).toBe("2026-10-15T12:00:00.000Z");
    expect(experimentsNow({ JAB_EXPERIMENTS_NOW: pinned, VERCEL_ENV: "preview" }).toISOString()).toBe(
      "2026-10-15T12:00:00.000Z"
    );
    // A pinned time in the past, so the real clock is told apart from it.
    const past = "2020-01-01T00:00:00Z";
    const before = Date.now();
    for (const env of [{ JAB_EXPERIMENTS_NOW: past, VERCEL_ENV: "production" }, { JAB_EXPERIMENTS_NOW: "soon" }, {}]) {
      expect(experimentsNow(env).getTime()).toBeGreaterThanOrEqual(before);
    }
  });

  it("reads dates as UTC days", () => {
    expect(dayStart("2026-11-02")).toBe(Date.UTC(2026, 10, 2));
    expect(Number.isNaN(dayStart("2026-13-01"))).toBe(true);
  });

  it("finds the experiment for a page, including dynamic segments", () => {
    const bills: Experiment = { ...OK, id: "bill-layout", path: "/bills/[id]" };
    expect(experimentForPath("/vote", [OK])).toBe(OK);
    expect(experimentForPath("/vote/", [OK])).toBe(OK);
    expect(experimentForPath("/vote/v/treatment", [OK])).toBeUndefined();
    expect(experimentForPath("/votes", [OK])).toBeUndefined();
    const home: Experiment = { ...OK, id: "home-layout", path: "/" };
    expect(experimentForPath("/", [home])).toBe(home);
    expect(experimentForPath("/vote", [home])).toBeUndefined();
    expect(experimentForPath("/v/treatment", [home])).toBeUndefined();
    expect(experimentForPath("/bills/hr-119-1", [bills])).toBe(bills);
    expect(experimentForPath("/bills/hr-119-1/text", [bills])).toBeUndefined();
    expect(findExperiment("bill-layout", [OK, bills])).toBe(bills);
    expect(findExperiment("nope", [OK])).toBeUndefined();
  });

  it("names the treatment route and the proxy matcher", () => {
    expect(treatmentPathname("/vote")).toBe("/vote/v/treatment");
    expect(treatmentPathname("/vote/")).toBe("/vote/v/treatment");
    expect(treatmentPathname("/bills/hr-119-1")).toBe("/bills/hr-119-1/v/treatment");
    expect(treatmentPathname("/")).toBe("/v/treatment");
    expect(matcherFor("/")).toBe("/");
    expect(matcherFor("/bills/[id]")).toBe("/bills/:id");
    expect(matcherFor("/vote")).toBe("/vote");
  });
});
