import { describe, expect, it } from "vitest";
import type { BillCardFacts, CardPassage } from "../types";
import {
  cardLead,
  chamberName,
  cosponsorParties,
  lawName,
  partyCountLabel,
  passageFailed,
  passageShort,
  whoItAffects,
} from "../vote-card";

const card = (crs: BillCardFacts["crs"]): BillCardFacts => ({ crs, passage: [], enacted: null, law_change_count: 0 });
const crs = { version_code: "49", action_date: "2026-05-29T00:00:00Z", action_desc: "Public Law", lead: "This act…" };
const roll = (over: Partial<CardPassage> = {}): CardPassage => ({
  chamber: "House",
  method: "roll",
  date: "2025-01-21T00:00:00Z",
  result: "Passed",
  yeas: 413,
  nays: 0,
  ...over,
});

describe("cardLead", () => {
  it("leads with the AI summary when there is one", () => {
    expect(cardLead({ bill_id: "x", short_summary: "Raises the age." }, card(crs))).toEqual({
      source: "ai",
      text: "Raises the age.",
    });
  });

  it("falls back to the CRS lead, labeled as CRS", () => {
    expect(cardLead({ bill_id: "x" }, card(crs))).toEqual({ source: "crs", text: "This act…" });
    expect(cardLead(null, card(crs))).toEqual({ source: "crs", text: "This act…" });
  });

  it("is null with neither summary", () => {
    expect(cardLead(null, card(null))).toBeNull();
    expect(cardLead(undefined, undefined)).toBeNull();
  });
});

describe("whoItAffects", () => {
  it("reads who_it_affects", () => {
    expect(whoItAffects({ bill_id: "x", who_it_affects: "New" })).toBe("New");
    expect(whoItAffects({ bill_id: "x" })).toBeUndefined();
    expect(whoItAffects(null)).toBeUndefined();
  });
});

describe("passageShort", () => {
  it.each<[string, CardPassage, string]>([
    ["a roll call's tally", roll(), "413–0"],
    ["a roll call without tallies", roll({ yeas: undefined, nays: undefined }), "roll call"],
    ["a voice vote", { chamber: "Senate", method: "voice", date: "2025-12-16T00:00:00Z" }, "voice vote"],
    ["unanimous consent", { chamber: "Senate", method: "uc", date: "2025-12-16T00:00:00Z" }, "unanimous consent"],
    ["a failed roll call", roll({ result: "Failed", yeas: 190, nays: 230 }), "failed 190–230"],
    ["a sustained veto", roll({ result: "Veto Sustained", yeas: 250, nays: 180 }), "failed 250–180"],
  ])("words %s", (_, p, want) => {
    expect(passageShort(p)).toBe(want);
  });
});

describe("passageFailed", () => {
  it.each([
    ["Passed", false],
    ["Agreed to", false],
    ["Bill Passed", false],
    ["Failed", true],
    ["Motion Rejected", true],
    ["Not Agreed to", true],
    ["Veto Sustained", true],
  ])("%s → %s", (result, want) => {
    expect(passageFailed(roll({ result }))).toBe(want);
  });

  it("treats a vote with no result as passed", () => {
    expect(passageFailed(roll({ result: undefined }))).toBe(false);
  });
});

describe("lawName", () => {
  it("names a public or private law, and nothing without a number", () => {
    expect(lawName({ date: "2025-12-26T00:00:00Z", law_type: "public", law_number: "119-62" })).toBe(
      "Public Law 119-62"
    );
    expect(lawName({ date: "2025-12-26T00:00:00Z", law_type: "private", law_number: "119-1" })).toBe(
      "Private Law 119-1"
    );
    expect(lawName({ date: "2025-12-26T00:00:00Z" })).toBeUndefined();
    expect(lawName(null)).toBeUndefined();
  });
});

describe("chamberName", () => {
  it("capitalizes the chamber however it's stored", () => {
    expect(chamberName("house")).toBe("House");
    expect(chamberName("SENATE")).toBe("Senate");
  });
});

describe("cosponsorParties", () => {
  const cosponsor = (party?: string) => ({ bioguideId: "X", name: "X", party, isOriginal: false });

  it("counts each party in A-to-Z order of its code, whatever the counts", () => {
    const parties = cosponsorParties([cosponsor("R"), cosponsor("D"), cosponsor("R"), cosponsor("I"), cosponsor()]);
    expect(parties).toEqual([
      { party: "D", count: 1 },
      { party: "I", count: 1 },
      { party: "R", count: 2 },
    ]);
    expect(parties.map(partyCountLabel)).toEqual(["1 Democrat", "1 independent", "2 Republicans"]);
  });

  it("keeps a party it has no name for as its code", () => {
    expect(partyCountLabel({ party: "L", count: 2 })).toBe("2 L");
    expect(partyCountLabel({ party: "ID", count: 2 })).toBe("2 independents");
  });
});
