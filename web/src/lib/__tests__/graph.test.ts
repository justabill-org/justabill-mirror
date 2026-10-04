import { describe, it, expect } from "vitest";
import {
  billLabel,
  billLabelFromId,
  compareTermsNewestFirst,
  congressYears,
  latestTerm,
  ordinal,
  relatedBillsFromJSON,
  tallyByParty,
  termYearsLabel,
} from "../graph";
import type { CompanionMemberVote, MemberTerm } from "../types";

describe("billLabel", () => {
  it("uses the display label for known types", () => {
    expect(billLabel("hr", 1)).toBe("H.R. 1");
    expect(billLabel("sjres", 12)).toBe("S.J.Res. 12");
  });

  it("upper-cases unknown types", () => {
    expect(billLabel("xyz", 3)).toBe("XYZ 3");
  });
});

describe("billLabelFromId", () => {
  it("labels a bill ID", () => {
    expect(billLabelFromId("s-119-1")).toBe("S. 1");
    expect(billLabelFromId("hjres-118-12")).toBe("H.J.Res. 12");
  });

  it("returns anything else unchanged", () => {
    expect(billLabelFromId("s-119")).toBe("s-119");
    expect(billLabelFromId("s-119-x")).toBe("s-119-x");
  });
});

describe("tallyByParty", () => {
  const vote = (id: string, vote: string, party?: string): CompanionMemberVote => ({
    member_id: id,
    first_name: "F",
    last_name: id,
    party,
    vote,
  });

  it("counts each vote value by party, major parties first and unknown last", () => {
    const got = tallyByParty([
      vote("a", "Yea", "R"),
      vote("b", "Nay", "D"),
      vote("c", "Yea", "D"),
      vote("d", "Present", "I"),
      vote("e", "Not Voting", "R"),
      vote("f", "Yea"),
      vote("g", "Nay", "L"),
      vote("h", "Guilty", "D"),
    ]);
    expect(got).toEqual([
      { party: "D", yea: 1, nay: 1, present: 0, notVoting: 0, other: 1 },
      { party: "R", yea: 1, nay: 0, present: 0, notVoting: 1, other: 0 },
      { party: "I", yea: 0, nay: 0, present: 1, notVoting: 0, other: 0 },
      { party: "L", yea: 0, nay: 1, present: 0, notVoting: 0, other: 0 },
      { party: "Unknown", yea: 1, nay: 0, present: 0, notVoting: 0, other: 0 },
    ]);
  });

  it("returns nothing for no votes", () => {
    expect(tallyByParty([])).toEqual([]);
  });
});

describe("relatedBillsFromJSON", () => {
  it("builds bill IDs and relation types from Congress.gov JSON", () => {
    const got = relatedBillsFromJSON([
      {
        congress: 119,
        number: 1,
        type: "S",
        title: "Senate companion",
        relationshipDetails: [
          { identifiedBy: "House", type: "Identical bill" },
          { identifiedBy: "Senate", type: "Identical bill" },
          { identifiedBy: "CRS", type: "Related bill" },
        ],
      },
      { congress: 119, number: 5, type: "HRES", title: "Rule", relationshipDetails: [] },
    ]);

    expect(got).toEqual([
      {
        bill_id: "s-119-1",
        congress: 119,
        bill_type: "s",
        number: 1,
        title: "Senate companion",
        relation_types: ["Identical bill", "Related bill"],
        shared_subjects: 0,
      },
      {
        bill_id: "hres-119-5",
        congress: 119,
        bill_type: "hres",
        number: 5,
        title: "Rule",
        relation_types: [],
        shared_subjects: 0,
      },
    ]);
  });

  it("merges duplicate entries for the same bill", () => {
    const got = relatedBillsFromJSON([
      { congress: 118, number: 2, type: "H.R.", title: "A", relationshipDetails: [{ identifiedBy: "CRS", type: "Related bill" }] },
      { congress: 118, number: 2, type: "HR", title: "A", relationshipDetails: [{ identifiedBy: "House", type: "Procedurally related" }] },
    ]);
    expect(got).toHaveLength(1);
    expect(got[0].bill_id).toBe("hr-118-2");
    expect(got[0].relation_types).toEqual(["Related bill", "Procedurally related"]);
  });

  it("returns an empty list for missing JSON", () => {
    expect(relatedBillsFromJSON(undefined)).toEqual([]);
  });
});

describe("ordinal", () => {
  it.each([
    [1, "1st"],
    [2, "2nd"],
    [3, "3rd"],
    [11, "11th"],
    [12, "12th"],
    [113, "113th"],
    [118, "118th"],
    [119, "119th"],
    [121, "121st"],
    [122, "122nd"],
  ])("%i → %s", (n, want) => {
    expect(ordinal(n)).toBe(want);
  });
});

describe("latestTerm", () => {
  const term = (congress: number, chamber = "House"): MemberTerm => ({
    member_id: "A000001",
    congress,
    chamber,
    state: "CA",
    party: "D",
  });

  it("picks the highest congress regardless of order", () => {
    expect(latestTerm([term(117), term(119, "Senate"), term(118)])?.congress).toBe(119);
  });

  it("returns undefined without terms", () => {
    expect(latestTerm([])).toBeUndefined();
  });

  it("of two chambers in one congress, picks the seat still held, whichever comes first", () => {
    const left = { ...term(119, "House"), end_date: "2025-06-30T00:00:00Z" };
    const held = { ...term(119, "Senate"), start_date: "2025-07-01T00:00:00Z" };
    expect(latestTerm([left, held])?.chamber).toBe("Senate");
    expect(latestTerm([held, left])?.chamber).toBe("Senate");
  });
});

describe("compareTermsNewestFirst", () => {
  const term = (congress: number, chamber: string, end_date?: string): MemberTerm => ({
    member_id: "A000001",
    congress,
    chamber,
    state: "CA",
    party: "D",
    ...(end_date ? { end_date } : {}),
  });

  it("puts later congresses first, then the held seat, then the later end", () => {
    const terms = [
      term(118, "House"),
      term(119, "House", "2025-03-01"),
      term(119, "Senate"),
      term(119, "House", "2025-09-01"),
    ];
    expect([...terms].sort(compareTermsNewestFirst).map((t) => `${t.congress} ${t.chamber} ${t.end_date ?? ""}`)).toEqual([
      "119 Senate ",
      "119 House 2025-09-01",
      "119 House 2025-03-01",
      "118 House ",
    ]);
  });
});

describe("congressYears", () => {
  it.each([
    [1, 1789, 1791],
    [118, 2023, 2025],
    [119, 2025, 2027],
  ])("the %ith Congress sits %i to %i", (congress, start, end) => {
    expect(congressYears(congress)).toEqual({ start, end });
  });
});

describe("termYearsLabel", () => {
  const base: MemberTerm = { member_id: "A000001", congress: 119, chamber: "House", state: "CA", party: "D" };

  it("uses the congress's years when the record has no dates", () => {
    expect(termYearsLabel(base)).toBe("2025–2027");
  });

  it("ends in the year of end_date, and starts in the year of start_date", () => {
    expect(termYearsLabel({ ...base, end_date: "2026-01-20T00:00:00Z" })).toBe("2025–2026");
    expect(termYearsLabel({ ...base, start_date: "2026-02-03" })).toBe("2026–2027");
    expect(termYearsLabel({ ...base, start_date: "2026-02-03", end_date: "2026-11-30" })).toBe("2026");
  });
});
