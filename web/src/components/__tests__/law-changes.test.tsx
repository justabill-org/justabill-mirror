import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { LawChanges } from "../bill/law-changes";
import type { BillLawChangesResponse, LawChangeEntry } from "@/lib/types";

// #316: the "Changes to current law" panel's states.

const amends: LawChangeEntry = {
  section_id: "/us/usc/t42/s1395w-4",
  in_us_code: true,
  loaded: true,
  title_number: 42,
  section_number: "1395w-4",
  heading: "Payment for physicians' services",
  change_kind: "amends",
  cite_text: "42 U.S.C. 1395w-4(t)",
  subsection_path: "(t)",
  instruction: "Section 1848(t) of the Social Security Act is amended by striking \"2026\" and inserting \"2027\".",
  explanation: "Extends the payment adjustment by one year.",
  also_changed_by: [
    { bill_id: "s-119-40", congress: 119, bill_type: "s", number: 40, title: "Other bill", ref_kinds: ["amends"] },
    { bill_id: "hr-119-12", congress: 119, bill_type: "hr", number: 12, title: "Third bill", ref_kinds: ["adds"] },
  ],
};

const unexplained: LawChangeEntry = {
  ...amends,
  section_id: "/us/usc/t26/s45",
  title_number: 26,
  section_number: "45",
  heading: null,
  loaded: false,
  change_kind: "repeals",
  subsection_path: null,
  instruction: null,
  explanation: null,
  also_changed_by: [],
};

const nonUSC: LawChangeEntry = {
  section_id: "nonusc:Section 5 of the Farm Act",
  in_us_code: false,
  loaded: false,
  title_number: null,
  section_number: null,
  heading: null,
  change_kind: "adds",
  cite_text: null,
  subsection_path: null,
  instruction: "Section 5 of the Farm Act is amended by adding at the end the following.",
  explanation: "Should not be shown.",
  also_changed_by: [],
};

const note: LawChangeEntry = {
  ...amends,
  section_id: "/us/usc/t10/s4271/note",
  is_note: true,
  loaded: false,
  title_number: 10,
  section_number: "4271",
  heading: null,
  cite_text: "10 U.S.C. 4271 note",
  subsection_path: null,
  instruction: "Section 829 of the National Defense Authorization Act for Fiscal Year 2017 is amended.",
  explanation: "Extends the pilot program by two years.",
  also_changed_by: [
    { bill_id: "s-119-41", congress: 119, bill_type: "s", number: 41, title: "Other bill", ref_kinds: ["amends"] },
  ],
};

const response = (over: Partial<BillLawChangesResponse> = {}): BillLawChangesResponse => ({
  bill_id: "hr-119-1",
  version_id: "hr-119-1-ih",
  version_code: "ih",
  explained: {
    model_used: "gemini-3.5-flash",
    prompt_version: "law-v1",
    generated_at: "2026-09-29T12:00:00Z",
    release_point: "119-111",
  },
  changes: [amends, unexplained, nonUSC],
  ai_generated: true,
  current_release_point: { release_point: "119-111", source_url: "https://uscode.house.gov/" },
  ...over,
});

const render = (data: BillLawChangesResponse | null, versionName?: string) =>
  renderToStaticMarkup(<LawChanges data={data} versionName={versionName} />);

describe("LawChanges", () => {
  it("renders nothing when the request failed or the list is empty", () => {
    expect(render(null)).toBe("");
    expect(render(response({ changes: [], explained: null, ai_generated: false }))).toBe("");
  });

  it("lists each section with its citation, heading, kind, explanation and instruction", () => {
    const html = render(response(), "Introduced in House");
    expect(html).toContain("Changes to current law");
    expect(html).toContain("<em>Introduced in House</em> text");
    expect(html).toContain("42 U.S.C. 1395w-4");
    expect(html).toContain("Payment for physicians&#x27; services");
    expect(html).toContain("Subsections (t)");
    expect(html).toContain(">Amends<");
    expect(html).toContain(">Repeals<");
    expect(html).toContain(">Adds<");
    expect(html).toContain("Extends the payment adjustment by one year.");
    expect(html).toContain("The bill&#x27;s instruction");
    expect(html).toContain("is amended by striking");
    expect(html).toContain("uscode.house.gov/view.xhtml?req=granuleid:USC-prelim-title42-section1395w-4");
  });

  it("offers the current text only for loaded sections", () => {
    const html = render(response());
    expect(html.match(/>Show current text</g)).toHaveLength(1);
    expect(html).toContain('aria-label="Show current text of 42 U.S.C. 1395w-4"');
    // The unloaded section still links to the source.
    expect(html).toContain("section45");
  });

  it("links the bills that also change a section", () => {
    const html = render(response());
    expect(html).toContain("Also changed by");
    expect(html).toContain('href="/bills/s-119-40"');
    expect(html).toContain('href="/bills/hr-119-12"');
    expect(html).toContain("S. 40");
    // Bill numbers are ink with tabular numerals, not amber monospace (#764).
    const number = /<a [^>]*href="\/bills\/s-119-40"[^>]*>/.exec(html)?.[0] ?? "";
    expect(number).toMatch(/class="[^"]*\btabular-nums\b[^"]*\btext-foreground\b/);
    expect(number).not.toMatch(/font-mono|text-link/);
  });

  it("labels the explanations as AI and names the release point", () => {
    const html = render(response());
    expect(html).toContain('role="note"');
    expect(html).toContain("AI-generated");
    expect(html).toContain('href="/methodology#law-changes"');
    expect(html).toContain("US Code current through Public Law 119-111.");
    expect(html).toContain("Explained by <em>gemini-3.5-flash</em> on September 29, 2026");
    expect(html).not.toContain("against the US Code through");
  });

  it("names an older release point the explanations were written against", () => {
    const html = render(
      response({
        explained: { ...response().explained!, release_point: "119-100" },
        current_release_point: { release_point: "119-111", source_url: "" },
      }),
    );
    expect(html).toContain("against the US Code through Public Law 119-100");
  });

  it("says when a section isn't explained", () => {
    const html = render(response());
    expect(html).toContain("No AI explanation of this change yet.");
  });

  it("shows a nonusc: law as not in the US Code, with the instruction only", () => {
    const html = render(response({ changes: [nonUSC] }));
    expect(html).toContain("Section 5 of the Farm Act");
    expect(html).toContain("Not in the US Code");
    expect(html).toContain("is amended by adding at the end");
    expect(html).not.toContain("Should not be shown.");
    expect(html).not.toContain("No AI explanation");
    expect(html).not.toContain("uscode.house.gov");
    expect(html).not.toContain("Show current text");
  });

  it("shows a statutory note as US Code material with its explanation, without the section's text", () => {
    const html = render(response({ changes: [note] }));
    expect(html).toContain(">10 U.S.C. 4271 note<");
    expect(html).toContain("Statutory note printed with the section");
    expect(html).not.toContain("Not in the US Code");
    expect(html).toContain("Extends the pilot program by two years.");
    expect(html).toContain("AI-generated");
    expect(html).toContain("is amended.");
    expect(html).toContain('href="/bills/s-119-41"');
    // GET /law serves the section, not the note, so there's no "current text" to open, even if a
    // section with the same title and number is loaded.
    expect(html).not.toContain("Show current text");
    expect(render(response({ changes: [{ ...note, loaded: true }] }))).not.toContain("Show current text");
    // The section's page on uscode.house.gov prints its notes after the text.
    expect(html).toContain("uscode.house.gov/view.xhtml?req=granuleid:USC-prelim-title10-section4271");
    expect(html).toContain("10 U.S.C. 4271 and its notes on uscode.house.gov");
  });

  it("leaves out the AI label when nothing is explained, and falls back to the version code", () => {
    const html = render(
      response({ explained: null, ai_generated: false, changes: [unexplained], current_release_point: null }),
    );
    expect(html).not.toContain('role="note"');
    expect(html).not.toContain("Explained by");
    expect(html).not.toContain("current through");
    expect(html).toContain("<em>ih</em> text");
    expect(render(response({ version_code: null }))).toContain("the bill&#x27;s latest text");
  });
});
