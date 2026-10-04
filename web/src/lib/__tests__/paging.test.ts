import { describe, it, expect } from "vitest";
import {
  MAX_CONGRESS,
  MAX_LIMIT,
  MAX_OFFSET,
  MAX_SEARCH_CHARS,
  MAX_SEARCH_OFFSET,
  clampSearch,
  lastReachableOffset,
  maxOffsetFor,
  pageCount,
  parseCongress,
  parseLimit,
  parseOffset,
} from "../paging";

describe("parseCongress", () => {
  it("reads a whole number from 1 to MAX_CONGRESS, as the API's queryCongress does (#881)", () => {
    expect(parseCongress("1")).toBe(1);
    expect(parseCongress("119")).toBe(119);
    expect(parseCongress("0119")).toBe(119);
    expect(parseCongress(String(MAX_CONGRESS))).toBe(MAX_CONGRESS);
  });

  it.each([undefined, null, "", "abc", "0", "-119", "+119", "119.0", "1e2", " 119", String(MAX_CONGRESS + 1), "99999999999999999999"])(
    "drops %j, which the API would refuse",
    (raw) => {
      expect(parseCongress(raw)).toBeUndefined();
    }
  );
});

describe("parseOffset", () => {
  it("reads a whole non-negative offset", () => {
    expect(parseOffset("24", 12)).toBe(24);
    expect(parseOffset("0", 12)).toBe(0);
    expect(parseOffset(String(MAX_OFFSET), 12)).toBe(MAX_OFFSET);
  });

  it("falls back to the first page for a missing, malformed or negative offset", () => {
    for (const raw of [undefined, "", "abc", "-20", "1.5", "12abc", "1e3", " "]) {
      expect(parseOffset(raw, 12), String(raw)).toBe(0);
    }
  });

  it("clamps an offset past the API's cap to the last reachable page", () => {
    expect(parseOffset("20000", 12)).toBe(9996);
    expect(parseOffset("10001", 20)).toBe(10_000);
    expect(parseOffset("99999999999999999999999", 12)).toBe(9996);
  });

  it("clamps a search's offset to the API's lower search cap (#619)", () => {
    expect(parseOffset("500", 20, MAX_SEARCH_OFFSET)).toBe(500);
    expect(parseOffset("504", 12, MAX_SEARCH_OFFSET)).toBe(492);
    expect(parseOffset("10000", 20, MAX_SEARCH_OFFSET)).toBe(500);
  });
});

describe("maxOffsetFor", () => {
  it("is the search cap only when there is a search", () => {
    expect(maxOffsetFor("tax")).toBe(MAX_SEARCH_OFFSET);
    for (const search of [undefined, null, ""]) {
      expect(maxOffsetFor(search), String(search)).toBe(MAX_OFFSET);
    }
  });
});

describe("clampSearch", () => {
  it("keeps a search the API takes, trimmed", () => {
    expect(clampSearch("clean water act")).toBe("clean water act");
    expect(clampSearch("  H.R. 1 ")).toBe("H.R. 1");
    expect(clampSearch("one two three four five six seven eight")).toBe("one two three four five six seven eight");
  });

  it("is undefined for no search or only spaces", () => {
    for (const raw of [undefined, "", "   ", "\t\u00a0\u3000"]) {
      expect(clampSearch(raw), JSON.stringify(raw)).toBeUndefined();
    }
  });

  it("keeps the first 8 terms, split where Go's strings.Fields splits", () => {
    expect(clampSearch("one two three four five six seven eight nine ten")).toBe(
      "one two three four five six seven eight"
    );
    // NEL and no-break space are spaces to Go; the byte order mark isn't.
    expect(clampSearch("a\u0085b\u00a0c\td\ne\u2003f\u3000g h i")).toBe("a b c d e f g h");
    expect(clampSearch("a\ufeffb")).toBe("a\ufeffb");
  });

  it("cuts a long search to 100 characters, counting code points", () => {
    expect(clampSearch("a".repeat(150))).toBe("a".repeat(MAX_SEARCH_CHARS));
    const emoji = "\u{1F5F3}".repeat(150);
    expect([...clampSearch(emoji)!]).toHaveLength(MAX_SEARCH_CHARS);
    expect(clampSearch(`${"a".repeat(99)} b`)).toBe("a".repeat(99));
  });
});

describe("parseLimit", () => {
  it("reads a page size and caps it at the API's maximum", () => {
    expect(parseLimit("24", 12)).toBe(24);
    expect(parseLimit("500", 12)).toBe(MAX_LIMIT);
  });

  it("falls back for a missing, malformed or zero limit", () => {
    for (const raw of [undefined, "", "abc", "-5", "0", "2.5"]) {
      expect(parseLimit(raw, 12), String(raw)).toBe(12);
    }
  });
});

describe("lastReachableOffset", () => {
  it("is the deepest page start at or below MAX_OFFSET", () => {
    expect(lastReachableOffset(12)).toBe(9996);
    expect(lastReachableOffset(20)).toBe(10_000);
    expect(lastReachableOffset(100)).toBe(10_000);
    expect(lastReachableOffset(12, MAX_SEARCH_OFFSET)).toBe(492);
  });
});

describe("pageCount", () => {
  it("reaches every page of a short list", () => {
    expect(pageCount(0, 12)).toEqual({ pages: 0, reachable: 0 });
    expect(pageCount(25, 12)).toEqual({ pages: 3, reachable: 3 });
  });

  it("stops at the page that starts at the last reachable offset", () => {
    expect(pageCount(50_000, 12)).toEqual({ pages: 4167, reachable: 834 });
    expect(pageCount(50_000, 20)).toEqual({ pages: 2500, reachable: 501 });
    // The page starting exactly at MAX_OFFSET is still reachable.
    expect(pageCount(10_001, 20)).toEqual({ pages: 501, reachable: 501 });
    expect(pageCount(5000, 12, MAX_SEARCH_OFFSET)).toEqual({ pages: 417, reachable: 42 });
  });
});
