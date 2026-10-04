import { describe, expect, it } from "vitest";
import { lawCitation, publicLawName, uscodeHouseUrl } from "../law-changes";
import type { LawChangeEntry } from "../types";

const entry = (over: Partial<LawChangeEntry>): LawChangeEntry => ({
  section_id: "/us/usc/t42/s1395w-4",
  in_us_code: true,
  loaded: true,
  title_number: 42,
  section_number: "1395w-4",
  heading: null,
  change_kind: "amends",
  cite_text: null,
  subsection_path: null,
  instruction: null,
  explanation: null,
  also_changed_by: [],
  ...over,
});

describe("lawCitation", () => {
  it("cites a US Code section by title and section", () => {
    expect(lawCitation(entry({}))).toBe("42 U.S.C. 1395w-4");
  });

  it("cites a statutory note under its section", () => {
    const note = { section_id: "/us/usc/t10/s4271/note", is_note: true, loaded: false };
    expect(lawCitation(entry({ ...note, title_number: 10, section_number: "4271" }))).toBe("10 U.S.C. 4271 note");
  });

  it("names a nonusc: law as the bill does", () => {
    expect(
      lawCitation(
        entry({
          section_id: "nonusc:Section 5 of the Social Security Act",
          in_us_code: false,
          loaded: false,
          title_number: null,
          section_number: null,
        }),
      ),
    ).toBe("Section 5 of the Social Security Act");
  });

  it("falls back to the cite text, then the ID", () => {
    const bare = { in_us_code: false, title_number: null, section_number: null };
    expect(lawCitation(entry({ ...bare, section_id: "nonusc:", cite_text: "the Act" }))).toBe("the Act");
    expect(lawCitation(entry({ ...bare, section_id: "nonusc:" }))).toBe("nonusc:");
    expect(lawCitation(entry({ ...bare, section_id: "odd" }))).toBe("odd");
  });
});

describe("uscodeHouseUrl", () => {
  it("links the section's granule on uscode.house.gov", () => {
    expect(uscodeHouseUrl(42, "1395w-4")).toBe(
      "https://uscode.house.gov/view.xhtml?req=granuleid:USC-prelim-title42-section1395w-4&num=0&edition=prelim",
    );
  });
});

describe("publicLawName", () => {
  it("names the release point's public law", () => {
    expect(publicLawName("119-111")).toBe("Public Law 119-111");
  });
});
