import { describe, it, expect } from "vitest";
import { congressGovAmendmentUrl, congressGovTextUrl, ordinal, trustMetadata, TRUST_LINKS } from "../trust";

describe("ordinal", () => {
  it.each([
    [1, "1st"],
    [2, "2nd"],
    [3, "3rd"],
    [4, "4th"],
    [11, "11th"],
    [12, "12th"],
    [13, "13th"],
    [101, "101st"],
    [102, "102nd"],
    [111, "111th"],
    [112, "112th"],
    [113, "113th"],
    [118, "118th"],
    [119, "119th"],
    [121, "121st"],
  ])("%i is %s", (n, want) => {
    expect(ordinal(n)).toBe(want);
  });
});

describe("congressGovTextUrl", () => {
  it.each([
    ["hr", "house-bill"],
    ["s", "senate-bill"],
    ["hjres", "house-joint-resolution"],
    ["sjres", "senate-joint-resolution"],
    ["hconres", "house-concurrent-resolution"],
    ["sconres", "senate-concurrent-resolution"],
    ["hres", "house-resolution"],
    ["sres", "senate-resolution"],
  ])("links a %s to Congress.gov's %s text page", (type, slug) => {
    expect(congressGovTextUrl({ congress: 119, bill_type: type as never, number: 42 })).toBe(
      `https://www.congress.gov/bill/119th-congress/${slug}/42/text`,
    );
  });

  it("returns undefined for an unknown type or a missing congress or number", () => {
    expect(congressGovTextUrl({ congress: 119, bill_type: "xyz" as never, number: 1 })).toBeUndefined();
    expect(congressGovTextUrl({ congress: 0, bill_type: "hr", number: 1 })).toBeUndefined();
    expect(congressGovTextUrl({ congress: 119, bill_type: "hr", number: 0 })).toBeUndefined();
  });
});

describe("congressGovAmendmentUrl", () => {
  // Checked on Congress.gov 2026-10-04: S.Amdt.12 to S.5 (119th) and H.Amdt.40 to H.R.21 (118th).
  it.each([
    [119, "samdt", 12, "https://www.congress.gov/amendment/119th-congress/senate-amendment/12"],
    [118, "hamdt", 40, "https://www.congress.gov/amendment/118th-congress/house-amendment/40"],
    [119, "SAMDT", 7, "https://www.congress.gov/amendment/119th-congress/senate-amendment/7"],
    [101, "HAMDT", 3, "https://www.congress.gov/amendment/101st-congress/house-amendment/3"],
  ])("links congress %i %s %i to %s", (congress, amendment_type, amendment_number, want) => {
    expect(congressGovAmendmentUrl({ congress, amendment_type, amendment_number })).toBe(want);
  });

  it("returns undefined for a type it can't build a URL for, or a missing congress or number", () => {
    expect(congressGovAmendmentUrl({ congress: 105, amendment_type: "suamdt", amendment_number: 1 })).toBeUndefined();
    expect(congressGovAmendmentUrl({ congress: 119, amendment_type: "", amendment_number: 1 })).toBeUndefined();
    expect(congressGovAmendmentUrl({ congress: 0, amendment_type: "samdt", amendment_number: 1 })).toBeUndefined();
    expect(congressGovAmendmentUrl({ congress: 119, amendment_type: "hamdt", amendment_number: 0 })).toBeUndefined();
  });
});

describe("trustMetadata", () => {
  it("gives each page its own title, description and canonical URL", () => {
    const md = trustMetadata("/privacy", "Privacy Policy", "What we keep.");
    expect(md.title).toBe("Privacy Policy | Just a Bill");
    expect(md.description).toBe("What we keep.");
    expect(md.alternates?.canonical).toBe("/privacy");
    expect(md.openGraph?.url).toBe("/privacy");
  });

  it("lists the five trust pages in footer order", () => {
    expect(TRUST_LINKS.map((l) => l.href)).toEqual(["/about", "/methodology", "/privacy", "/terms", "/contact"]);
  });
});
