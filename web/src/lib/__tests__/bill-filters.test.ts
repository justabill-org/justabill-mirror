import { describe, expect, it } from "vitest";
import {
  type BillFilters,
  billFiltersQuery,
  billListParams,
  parseBillFilters,
  searchParamsOf,
  voteHref,
} from "../bill-filters";
import { MAX_CONGRESS } from "../paging";

// The filters /bills and /vote share, read from and written to the URL (#785, #796).

const DEFAULTS: BillFilters = { view: "laws", sort: "latest_action", congress: undefined };

function parse(query: string): BillFilters {
  return parseBillFilters(new URLSearchParams(query));
}

describe("parseBillFilters", () => {
  it("opens on Laws, latest action first, in the current congress", () => {
    expect(parse("")).toEqual(DEFAULTS);
  });

  it("reads every filter", () => {
    expect(
      parse("show=passed&sort=introduced_date&congress=118&type=hr&chamber=senate&area=Health&q=water"),
    ).toEqual({
      view: "passed",
      sort: "introduced_date",
      congress: 118,
      type: "hr",
      chamber: "senate",
      area: "Health",
      q: "water",
    });
  });

  it("reads congress=all as every congress", () => {
    expect(parse("congress=all").congress).toBe("all");
  });

  it("falls back to the defaults for values a hand-edited URL makes up (Review Focus 3)", () => {
    expect(parse("show=xyz&type=zz&chamber=both&congress=abc&sort=1&q=%20%20")).toEqual(DEFAULTS);
  });

  it.each(["0", "-3", "+118", "1.5", "1e2", "118abc", " ", "201", "99999999999999999999"])("ignores congress=%j", (value) => {
    expect(parse(`congress=${encodeURIComponent(value)}`).congress).toBeUndefined();
  });

  it("keeps a congress from 1 to MAX_CONGRESS, the API's range (#881)", () => {
    expect(parse("congress=1").congress).toBe(1);
    expect(parse(`congress=${MAX_CONGRESS}`).congress).toBe(MAX_CONGRESS);
    expect(parse("congress=0118").congress).toBe(118);
  });

  it("keeps an unknown policy area, so the list says No bills found rather than showing every bill", () => {
    expect(parse("area=Nope").area).toBe("Nope");
  });

  it("cuts a policy area to the 100 characters the API takes, so it never answers 400", () => {
    const area = parse(`area=${"é".repeat(150)}`).area;
    expect(area).toBe("é".repeat(100));
  });

  it("drops an empty policy area", () => {
    expect(parse("area=").area).toBeUndefined();
  });

  it("clamps a search to the API's limits", () => {
    expect(parse("q=one two three four five six seven eight nine").q).toBe("one two three four five six seven eight");
  });
});

describe("billFiltersQuery", () => {
  it("is empty for the defaults", () => {
    expect(billFiltersQuery(DEFAULTS)).toBe("");
  });

  it("writes every filter that isn't a default, in a fixed order", () => {
    expect(
      billFiltersQuery({
        view: "committee",
        sort: "introduced_date",
        congress: "all",
        type: "s",
        chamber: "house",
        area: "Armed Forces and National Security",
        q: "a&b",
      }),
    ).toBe(
      "show=committee&sort=introduced_date&congress=all&type=s&chamber=house" +
        "&area=Armed+Forces+and+National+Security&q=a%26b",
    );
  });

  it("round-trips through parseBillFilters", () => {
    const f: BillFilters = { view: "all", sort: "latest_action", congress: 117, area: "Taxation", q: "x" };
    expect(parse(billFiltersQuery(f))).toEqual(f);
  });
});

describe("billListParams", () => {
  it("lists the current congress when the URL names none", () => {
    expect(billListParams(DEFAULTS, 119)).toEqual({ sort: "latest_action", congress: 119 });
  });

  it("lists every congress for congress=all, and when the current one isn't known", () => {
    expect(billListParams({ ...DEFAULTS, congress: "all" }, 119).congress).toBeUndefined();
    expect(billListParams(DEFAULTS, undefined).congress).toBeUndefined();
  });

  it("names the policy area as the API's policy_area", () => {
    expect(
      billListParams({ ...DEFAULTS, congress: 118, type: "hr", chamber: "house", area: "Health", q: "dog" }, 119),
    ).toEqual({ sort: "latest_action", congress: 118, type: "hr", chamber: "house", policy_area: "Health", q: "dog" });
  });
});

describe("searchParamsOf", () => {
  it("keeps the first of a repeated parameter and leaves out missing ones", () => {
    expect(searchParamsOf({ show: ["passed", "all"], q: undefined, area: "Health" }).toString()).toBe(
      "show=passed&area=Health",
    );
  });
});

describe("voteHref", () => {
  it("is /vote for the defaults, and /vote with the same query otherwise", () => {
    expect(voteHref(DEFAULTS)).toBe("/vote");
    expect(voteHref({ ...DEFAULTS, view: "passed", type: "hr" })).toBe("/vote?show=passed&type=hr");
  });
});
