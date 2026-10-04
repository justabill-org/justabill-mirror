import { describe, expect, it } from "vitest";
import {
  congressHref,
  congressLabel,
  congressYearsLabel,
  congressParam,
  currentCongress,
  defaultedCongresses,
  selectedCongresses,
  switchCongresses,
} from "../congress";
import type { Congress } from "../types";

function congress(number: number, is_current = false): Congress {
  return { number, start_date: "", is_current };
}

describe("currentCongress", () => {
  it("picks the congress the API marks current", () => {
    expect(currentCongress([congress(120), congress(119, true), congress(118)])?.number).toBe(119);
  });

  it("falls back to the highest-numbered congress", () => {
    expect(currentCongress([congress(118), congress(120), congress(119)])?.number).toBe(120);
  });

  it("is undefined without congresses", () => {
    expect(currentCongress([])).toBeUndefined();
  });
});

describe("congressLabel", () => {
  it("names a congress by its two years and its number", () => {
    expect([congressLabel(119), congressLabel(118), congressLabel(100), congressLabel(121)]).toEqual([
      "2025–26 (119th)",
      "2023–24 (118th)",
      "1987–88 (100th)",
      "2029–30 (121st)",
    ]);
  });
});

describe("congressYearsLabel", () => {
  it("names a congress by its two years alone", () => {
    expect([congressYearsLabel(119), congressYearsLabel(118), congressYearsLabel(106)]).toEqual([
      "2025–26",
      "2023–24",
      "1999–00",
    ]);
  });
});

describe("switchCongresses", () => {
  it("offers only congresses with votes, newest first", () => {
    const list: Congress[] = [
      { ...congress(118), has_votes: true },
      { ...congress(120), has_votes: false },
      congress(117),
      { ...congress(119, true), has_votes: true },
    ];
    expect(switchCongresses(list)).toEqual([119, 118]);
  });

  it("offers nothing when the API doesn't say which congresses have votes", () => {
    expect(switchCongresses([congress(119, true), congress(118)])).toEqual([]);
  });
});

describe("selectedCongresses", () => {
  const offered = [119, 118];

  it("is empty, meaning every congress, without a filter", () => {
    expect(selectedCongresses([], offered)).toEqual([]);
  });

  it("keeps offered congresses, newest first, and drops the rest", () => {
    expect(selectedCongresses(["118"], offered)).toEqual([118]);
    expect(selectedCongresses(["117", "abc", "118", "118"], offered)).toEqual([118]);
    expect(selectedCongresses(["117"], offered)).toEqual([]);
  });

  it("treats a filter naming every offered congress as all of them", () => {
    expect(selectedCongresses(["118", "119"], offered)).toEqual([]);
  });
});

describe("defaultedCongresses (#717)", () => {
  const offered = [119, 118];

  it("is the newest offered congress without a filter, or with one naming none offered", () => {
    expect(defaultedCongresses([], offered)).toEqual([119]);
    expect(defaultedCongresses(["117", "abc"], offered)).toEqual([119]);
  });

  it("keeps the congress the filter names", () => {
    expect(defaultedCongresses(["118"], offered)).toEqual([118]);
  });

  it("is empty, meaning every congress, for all or a filter naming each one", () => {
    expect(defaultedCongresses(["all"], offered)).toEqual([]);
    expect(defaultedCongresses(["118", "119"], offered)).toEqual([]);
    expect(defaultedCongresses([], [])).toEqual([]);
  });
});

describe("congressParam (#717)", () => {
  it("leaves the default out of the URL and keeps any other choice", () => {
    expect(congressParam("119", 119)).toBeNull();
    expect(congressParam("", 119)).toBeNull();
    expect(congressParam("118", 119)).toBe("118");
    expect(congressParam("all", 119)).toBe("all");
    expect(congressParam("119", undefined)).toBe("119");
  });
});

describe("congressHref", () => {
  it("replaces the congress filter and keeps other parameters", () => {
    expect(congressHref("/scorecard", "congress=119&ref=share", [118])).toBe("/scorecard?ref=share&congress=118");
    expect(congressHref("/scorecard", "congress=119&congress=118", [])).toBe("/scorecard");
    expect(congressHref("/vote", "", [119, 118])).toBe("/vote?congress=119&congress=118");
  });
});
