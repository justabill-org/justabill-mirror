import { describe, expect, it } from "vitest";
import { contentsLabel, listsChildrenInContents, sectionHeading } from "@/lib/bill-text";
import type { BillTextSection } from "@/lib/types";

function unit(fields: Partial<BillTextSection>): BillTextSection {
  return { id: "x", header: "", content: "", ...fields };
}

describe("sectionHeading", () => {
  it("joins a section's designation and header with a space", () => {
    expect(sectionHeading(unit({ enum: "Sec. 101.", header: "Short title" }))).toBe("Sec. 101. Short title");
    expect(sectionHeading(unit({ enum: "(a)", header: "In general" }))).toBe("(a) In general");
  });

  it("joins a title's designation and header with a dash", () => {
    expect(sectionHeading(unit({ enum: "Title I", header: "Agriculture" }))).toBe("Title I—Agriculture");
  });

  it("uses whichever it has, and the header alone for rows stored before designations", () => {
    expect(sectionHeading(unit({ enum: "Sec. 3." }))).toBe("Sec. 3.");
    expect(sectionHeading(unit({ header: "Short title" }))).toBe("Short title");
    expect(sectionHeading(unit({}))).toBe("");
  });
});

describe("contentsLabel", () => {
  it("falls back to the start of the text", () => {
    const text = "Strike all after the enacting clause and insert the following: and a good deal more";
    expect(contentsLabel(unit({ kind: "text", content: text }))).toBe(`${text.slice(0, 60)}…`);
    expect(contentsLabel(unit({ content: "  Short\n text. " }))).toBe("Short text.");
  });
});

describe("listsChildrenInContents", () => {
  it("lists what's inside titles and divisions, not a section's subsections", () => {
    expect(listsChildrenInContents(unit({ kind: "title" }))).toBe(true);
    expect(listsChildrenInContents(unit({}))).toBe(true);
    expect(listsChildrenInContents(unit({ kind: "section" }))).toBe(false);
  });
});
