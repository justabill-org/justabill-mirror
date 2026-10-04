import { describe, it, expect } from "vitest";
import {
  aggregateCardCopy,
  aggregateShareUrl,
  billCardCopy,
  billShareUrl,
  memberLabel,
  normalizePositionVote,
  parseBillId,
  isShareableCell,
  parseAggregateShare,
  parseBillShare,
  plainText,
  shareImageUrl,
  truncateTitle,
  MAX_CARD_TITLE_LENGTH,
  type CardMember,
} from "../share";

const senator: CardMember = { firstName: "Jane", lastName: "Doe", chamber: "Senate", state: "NY", party: "D" };
const rep: CardMember = { firstName: "John", lastName: "Roe", chamber: "House", state: "TX", district: 2, party: "R" };

describe("parseBillShare", () => {
  it.each([
    ["hr-119-1", "yea", undefined, { billId: "hr-119-1", vote: "yea" }],
    ["s-118-2044", "nay", undefined, { billId: "s-118-2044", vote: "nay" }],
    ["hjres-119-12", "yea", "X000002", { billId: "hjres-119-12", vote: "yea", memberId: "X000002" }],
  ])("accepts %s/%s/%s", (bill, vote, member, want) => {
    expect(parseBillShare(bill, vote, member)).toEqual(want);
  });

  it.each([
    ["HR-119-1", "yea", undefined], // uppercase
    ["hr-119-01", "yea", undefined], // leading zero
    ["hr-0119-1", "yea", undefined],
    ["hr-119-0", "yea", undefined],
    ["xx-119-1", "yea", undefined], // unknown bill type
    ["hr-119-123456", "yea", undefined],
    ["hr-119-1", "skip", undefined], // a skip has nothing to share
    ["hr-119-1", "Yea", undefined],
    ["hr-119-1", "present", undefined],
    ["hr-119-1", "yea", "x000002"],
    ["hr-119-1", "yea", ""],
  ])("refuses %s/%s/%s", (bill, vote, member) => {
    expect(parseBillShare(bill, vote, member)).toBeNull();
  });

  it("round-trips through billShareUrl", () => {
    for (const share of [
      { billId: "hr-119-1", vote: "yea" as const },
      { billId: "s-119-5", vote: "nay" as const, memberId: "X000002" },
    ]) {
      const [, , , bill, vote, member] = billShareUrl(share).split("/");
      expect(parseBillShare(bill, vote, member)).toEqual(share);
    }
    expect(billShareUrl({ billId: "hr-119-1", vote: "yea", memberId: "X000002" })).toBe(
      "/share/bill/hr-119-1/yea/X000002"
    );
    expect(shareImageUrl("/share/bill/hr-119-1/yea")).toBe("/share/bill/hr-119-1/yea/image.png");
  });

  it("parses bill IDs", () => {
    expect(parseBillId("sconres-119-7")).toEqual({ billType: "sconres", congress: 119, number: 7 });
    expect(parseBillId("hr-119")).toBeNull();
    // Only the eight bill types, not names every object inherits (#634).
    expect(parseBillId("constructor-119-1")).toBeNull();
    expect(parseBillId("tostring-119-1")).toBeNull();
  });
});

describe("memberLabel", () => {
  it.each<[CardMember, string]>([
    [senator, "Sen. Jane Doe (D-NY)"],
    [rep, "Rep. John Roe (R-TX-2)"],
    [{ ...rep, state: "AK", district: 0 }, "Rep. John Roe (R-AK-AL)"],
    [{ ...rep, state: "WY", district: undefined }, "Rep. John Roe (R-WY-AL)"],
    [{ ...rep, state: "DC", district: 0, party: "D" }, "Del. John Roe (D-DC)"],
    [{ ...rep, state: "PR", district: 0, party: "D" }, "Res. Comm. John Roe (D-PR)"],
    [{ ...senator, party: "I", state: "VT" }, "Sen. Jane Doe (I-VT)"],
    [{ ...senator, party: "O" }, "Sen. Jane Doe (NY)"], // unknown party letters aren't printed
  ])("labels %o", (member, want) => {
    expect(memberLabel(member)).toBe(want);
  });
});

describe("billCardCopy", () => {
  const bill = { billType: "hr", number: 1, congress: 119, title: "Example Act of 2025" };

  it("prints the bill and the sharer's vote", () => {
    const copy = billCardCopy(bill, "yea");
    expect(copy.eyebrow).toBe("H.R. 1 · 119th Congress");
    expect(copy.title).toBe("Example Act of 2025");
    expect(plainText(copy.headline)).toBe("I'd vote Yea");
    expect(copy.summary).toBe("I'd vote Yea on H.R. 1");
    expect(copy.detail).toBeUndefined();
    expect(copy.plain).toBe("H.R. 1 · 119th Congress. Example Act of 2025. I'd vote Yea.");
  });

  it.each([
    ["nay", "Rep. John Roe (R-TX-2) voted Nay"],
    ["yea", "Rep. John Roe (R-TX-2) voted Yea"],
    ["present", "Rep. John Roe (R-TX-2) voted Present"],
    ["not_voting", "Rep. John Roe (R-TX-2) did not vote"],
    [null, "Rep. John Roe (R-TX-2) has no recorded vote on this bill"],
  ] as const)("prints the member's position %s", (position, want) => {
    const copy = billCardCopy(bill, "yea", { member: rep, position });
    expect(plainText(copy.detail ?? [])).toBe(want);
  });

  it("shortens long titles and doesn't double the punctuation", () => {
    const long = `To provide for ${"reconciliation and appropriations ".repeat(8)}for fiscal year 2026.`;
    const copy = billCardCopy({ ...bill, title: long }, "nay");
    expect(copy.title?.length).toBeLessThanOrEqual(MAX_CARD_TITLE_LENGTH);
    expect(copy.title?.endsWith("…")).toBe(true);
    expect(copy.plain).toContain("… I'd vote Nay.");
  });
});

describe("truncateTitle", () => {
  it("keeps short titles and collapses whitespace", () => {
    expect(truncateTitle("  Example   Act ")).toBe("Example Act");
  });

  it("cuts at a word boundary", () => {
    expect(truncateTitle("alpha beta gamma delta", 15)).toBe("alpha beta…");
  });

  it("cuts inside a word that is longer than half the limit", () => {
    expect(truncateTitle("a supercalifragilistic", 12)).toBe("a supercali…");
  });
});

describe("normalizePositionVote", () => {
  it.each([
    ["yea", "yea"],
    ["Yea", "yea"],
    ["Aye", "yea"],
    ["No", "nay"],
    ["nay", "nay"],
    ["Present", "present"],
    ["Not Voting", "not_voting"],
    ["not_voting", "not_voting"],
    ["", null],
    [undefined, null],
    ["Guilty", null],
  ] as const)("%s → %s", (vote, want) => {
    expect(normalizePositionVote(vote)).toBe(want);
  });
});

describe("parseAggregateShare (#166)", () => {
  it.each([
    ["hr-119-1", "national", { billId: "hr-119-1", scopeKey: "" }],
    ["hr-119-1", "CA", { billId: "hr-119-1", scopeKey: "CA" }],
    ["hr-119-1", "CA-12", { billId: "hr-119-1", scopeKey: "CA-12" }],
    ["s-118-42", "AK-0", { billId: "s-118-42", scopeKey: "AK-0" }],
  ])("accepts %s/%s", (bill, scope, want) => {
    expect(parseAggregateShare(bill, scope)).toEqual(want);
    // One URL per card: building the URL again gives the same path.
    expect(aggregateShareUrl(want)).toBe(`/share/aggregate/${bill}/${scope}`);
  });

  it.each([
    ["HR-119-1", "CA"], // non-canonical bill
    ["xx-119-1", "CA"], // unknown bill type
    ["hr-119-1", ""],
    ["hr-119-1", "ca"], // lowercase state
    ["hr-119-1", "CA-012"], // leading zero
    ["hr-119-1", "CA-00"],
    ["hr-119-1", "CA-123"],
    ["hr-119-1", "CAL"],
    ["hr-119-1", "National"],
    ["hr-119-1", "us"],
  ])("refuses %s/%s", (bill, scope) => {
    expect(parseAggregateShare(bill, scope)).toBeNull();
  });
});

describe("aggregateCardCopy (#166)", () => {
  const cell = {
    scope: "district" as const,
    scope_key: "CA-12",
    status: "published" as const,
    yea_pct: 62,
    nay_pct: 38,
    voters_floor: 1340,
    published_at: "2026-10-01T23:30:00Z",
  };
  const bill = { billType: "hr", number: 1, congress: 119, title: "Example Act of 2025" };

  it("prints the cell's numbers, the users counted and the not-a-poll label", () => {
    const copy = aggregateCardCopy(bill, cell);
    expect(copy.summary).toBe("Just a Bill users in CA-12: 62% Yea, 38% Nay on H.R. 1");
    expect(copy.eyebrow).toBe("H.R. 1 · 119th Congress");
    // No-break spaces keep each number with its vote on the image.
    expect(plainText(copy.headline)).toBe("Just a Bill users in CA-12: 62%\u00a0Yea, 38%\u00a0Nay");
    expect(copy.headline.filter((r) => r.strong).map((r) => r.text)).toEqual(["62%\u00a0Yea, ", "38%\u00a0Nay"]);
    // Dates are UTC (#454), so a late-evening publish isn't a day off.
    expect(plainText(copy.detail ?? [])).toBe("1,340+ users · published Oct 1, 2026");
    expect(copy.note).toMatch(/^Just a Bill users, not a poll\./);
    expect(copy.plain).toBe(
      "H.R. 1 · 119th Congress. Example Act of 2025. Just a Bill users in CA-12: 62%\u00a0Yea, 38%\u00a0Nay. " +
        "1,340+ users · published Oct 1, 2026. Just a Bill users, not a poll. Opt-in users who chose to vote, " +
        "not a sample of residents."
    );
    expect(copy.methodology).toEqual({ label: "How these numbers work", path: "/methodology#aggregates" });
  });

  it("names the national cell, a state and an at-large seat", () => {
    expect(aggregateCardCopy(bill, { ...cell, scope: "national", scope_key: "" }).summary).toMatch(
      /^Just a Bill users nationwide:/
    );
    expect(aggregateCardCopy(bill, { ...cell, scope: "state", scope_key: "TX" }).summary).toMatch(
      /^Just a Bill users in TX:/
    );
    expect(aggregateCardCopy(bill, { ...cell, scope_key: "WY-0" }).summary).toMatch(
      /^Just a Bill users in WY at-large:/
    );
  });

  it("leaves out the detail line when the count and date are missing", () => {
    expect(aggregateCardCopy(bill, { ...cell, voters_floor: null, published_at: null }).detail).toBeUndefined();
    expect(plainText(aggregateCardCopy(bill, { ...cell, voters_floor: null }).detail ?? [])).toBe(
      "published Oct 1, 2026"
    );
  });

  it("shares only published cells with numbers", () => {
    expect(isShareableCell(cell)).toBe(true);
    expect(isShareableCell({ ...cell, status: "held" })).toBe(false);
    expect(isShareableCell({ ...cell, yea_pct: null })).toBe(false);
    expect(isShareableCell(null)).toBe(false);
  });
});
