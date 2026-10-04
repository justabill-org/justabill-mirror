import { describe, expect, it } from "vitest";
import { billChangedSince, crsAbstract, crsPreview, crsSummaryUrl } from "../crs";
import type { BillTextVersion } from "../types";

const para = (n: number, len: number) => `${n}`.repeat(len);

describe("crsPreview", () => {
  it("keeps whole paragraphs up to about 600 characters", () => {
    const text = [para(1, 300), para(2, 250), para(3, 300)].join("\n\n");
    const { preview, truncated } = crsPreview(text);
    expect(preview).toBe([para(1, 300), para(2, 250)].join("\n\n"));
    expect(truncated).toBe(true);
  });

  it("shows a short summary whole", () => {
    const text = "This bill renames a post office.\n\n• First item";
    expect(crsPreview(text)).toEqual({ preview: text, truncated: false });
  });

  it("always shows the first paragraph, however long", () => {
    const text = [para(1, 900), para(2, 10)].join("\n\n");
    expect(crsPreview(text)).toEqual({ preview: para(1, 900), truncated: true });
  });

  it("treats blank lines with spaces as paragraph breaks", () => {
    expect(crsPreview("a\n \nb", 1)).toEqual({ preview: "a", truncated: true });
  });
});

describe("crsAbstract", () => {
  it("is the first paragraph", () => {
    expect(crsAbstract("\nThis bill does a thing.\n\n• First item")).toBe("This bill does a thing.");
  });

  it("is undefined for empty text", () => {
    expect(crsAbstract("  ")).toBeUndefined();
  });
});

describe("crsSummaryUrl", () => {
  it("links the version's summary on Congress.gov", () => {
    expect(crsSummaryUrl({ congress: 119, bill_type: "s", number: 5 }, { version_code: "00" })).toBe(
      "https://www.congress.gov/bill/119th-congress/senate-bill/5/summary/00",
    );
  });

  it("is undefined for an unknown bill type", () => {
    expect(crsSummaryUrl({ congress: 119, bill_type: "x" as "s", number: 5 }, { version_code: "00" })).toBeUndefined();
  });
});

describe("billChangedSince", () => {
  const version = (date?: string): BillTextVersion => ({
    id: "v",
    bill_id: "s-119-5",
    version_type: "Introduced in Senate",
    version_code: "is",
    date,
    formats: [],
    sort_order: 0,
  });
  const summary = { action_date: "2025-06-05T00:00:00Z" };

  it("is true when a text version is newer than the summary's action", () => {
    expect(billChangedSince(summary, [version("2025-06-05T04:00:00Z"), version("2025-09-10T04:00:00Z")])).toBe(true);
  });

  it("ignores the text of the action's own day and undated versions", () => {
    expect(billChangedSince(summary, [version("2025-06-05T04:00:00Z"), version()])).toBe(false);
  });

  it("is false without text versions", () => {
    expect(billChangedSince(summary, [])).toBe(false);
  });
});
