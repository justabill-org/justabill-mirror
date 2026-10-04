import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import {
  constituencyKeys,
  findCell,
  ownConstituency,
  pickAlignment,
  pickerCells,
  repScopeKey,
  scopeName,
  votersLabel,
} from "../aggregates";
import type { LocalRep } from "../local/reps";
import type { AggregateCell, BillAggregatesResponse, RepAlignment } from "../types";

// #126: the helpers behind the "How Just a Bill users voted" panel and the scorecard line.

function cell(scopeKey: string, overrides: Partial<AggregateCell> = {}): AggregateCell {
  return {
    scope: scopeKey === "" ? "national" : scopeKey.includes("-") ? "district" : "state",
    scope_key: scopeKey,
    status: "published",
    yea_pct: 60,
    nay_pct: 40,
    voters_floor: 120,
    published_at: "2026-10-01T12:00:00Z",
    ...overrides,
  };
}

const data: BillAggregatesResponse = {
  bill_id: "hr-119-1",
  as_of: "2026-10-01T12:00:00Z",
  national: cell(""),
  states: [cell("CA"), cell("TX")],
  districts: [cell("CA-12"), cell("WY-0")],
};

describe("scopeName", () => {
  it.each([
    ["", "Nationwide"],
    ["CA", "CA"],
    ["CA-12", "CA-12"],
    ["WY-0", "WY at-large"],
    ["DC-0", "DC delegate (non-voting)"],
  ])("names %j as %j", (key, want) => {
    expect(scopeName(key)).toBe(want);
  });
});

describe("ownConstituency", () => {
  const local = { state: "TX", district: 10 };

  it("prefers the account's district, where the votes count", () => {
    expect(ownConstituency({ state: "CA", district: 12 }, local)).toEqual({ state: "CA", district: 12 });
  });

  it("falls back to the reps saved in this browser", () => {
    expect(ownConstituency(null, local)).toEqual(local);
    expect(ownConstituency({}, local)).toEqual(local);
  });

  it("is null when neither is known", () => {
    expect(ownConstituency(null, null)).toBeNull();
  });

  it("keeps a state without a district", () => {
    expect(ownConstituency({ state: "CA" }, null)).toEqual({ state: "CA", district: null });
  });
});

describe("constituencyKeys", () => {
  it("lists the district, then the state", () => {
    expect(constituencyKeys({ state: "CA", district: 12 })).toEqual(["CA-12", "CA"]);
    expect(constituencyKeys({ state: "AK", district: 0 })).toEqual(["AK-0", "AK"]);
  });

  it("lists only the state without a district, and nothing without a constituency", () => {
    expect(constituencyKeys({ state: "CA", district: null })).toEqual(["CA"]);
    expect(constituencyKeys(null)).toEqual([]);
  });
});

describe("cells", () => {
  it("offers states, then districts", () => {
    expect(pickerCells(data).map((c) => c.scope_key)).toEqual(["CA", "TX", "CA-12", "WY-0"]);
  });

  it("finds a served cell, and null for one that isn't served", () => {
    expect(findCell(data, "CA-12")?.scope_key).toBe("CA-12");
    expect(findCell(data, "NY")).toBeNull();
  });

  it("labels the rounded count", () => {
    expect(votersLabel(1340)).toBe("1,340+ users");
    expect(votersLabel(null)).toBeNull();
  });
});

describe("pickAlignment", () => {
  const house: LocalRep = { id: "H1", name: "Hal House", party: "D", chamber: "House", state: "CA", district: 12 };
  const senator: LocalRep = { id: "S1", name: "Sue Senate", party: "R", chamber: "Senate", state: "CA" };

  function row(overrides: Partial<RepAlignment>): RepAlignment {
    return {
      member_id: "H1",
      congress: 119,
      scope_key: "CA-12",
      bills_compared: 22,
      bills_agreed: 14,
      computed_at: "2026-10-01T12:00:00Z",
      ...overrides,
    };
  }

  it("measures a rep in their district and a senator in their state", () => {
    expect(repScopeKey(house)).toBe("CA-12");
    expect(repScopeKey(senator)).toBe("CA");
    expect(repScopeKey({ ...house, district: undefined })).toBeNull();
  });

  it("picks the newest congress in the visitor's constituency", () => {
    const rows = [row({ congress: 118 }), row({ congress: 119, bills_agreed: 3 }), row({ scope_key: "CA-11", congress: 120 })];
    expect(pickAlignment(rows, house)?.bills_agreed).toBe(3);
  });

  it("keeps to the congresses the switch picked", () => {
    const rows = [row({ congress: 118, bills_agreed: 5 }), row({ congress: 119 })];
    expect(pickAlignment(rows, house, [118])?.bills_agreed).toBe(5);
    expect(pickAlignment(rows, house, [117])).toBeNull();
  });

  it("skips rows with nothing compared, and reps with no seat to measure", () => {
    expect(pickAlignment([row({ bills_compared: 0, bills_agreed: 0 })], house)).toBeNull();
    expect(pickAlignment([row({})], { ...house, district: null })).toBeNull();
  });
});

describe("methodology rules", () => {
  // The Methodology page states the job's defaults; this fails when they drift apart.
  const repoRoot = path.resolve(__dirname, "../../../..");
  const config = readFileSync(path.join(repoRoot, "pipeline/internal/aggregates/config.go"), "utf8");
  const page = readFileSync(path.join(repoRoot, "web/src/app/(public)/(trust)/methodology/page.tsx"), "utf8");

  function defaultOf(name: string): string {
    const m = new RegExp(`${name}\\s*=\\s*(\\d+)`).exec(config);
    if (!m) throw new Error(`${name} not found in config.go`);
    return m[1];
  }

  it.each([
    ["defaultMinAccountAgeHours", "at least {} hours old"],
    ["defaultMinCellVotes", "{} counted votes for a state or district"],
    ["defaultMinNationalVotes", "{} nationwide"],
    ["defaultRepublishMinChanges", "at least {} votes have changed"],
  ])("matches %s", (name, phrase) => {
    expect(page).toContain(phrase.replace("{}", defaultOf(name)));
  });
});
