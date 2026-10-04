import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { BillSummary } from "../bill/bill-summary";

const summary = {
  bill_id: "119-hr-42",
  short_summary: "Would rename a post office.",
  generated_at: "2026-09-20T23:30:00Z",
};
const officialTextUrl = "https://www.congress.gov/bill/119th-congress/house-bill/42/text";

describe("BillSummary", () => {
  it("shows the AI disclaimer, the generation date and the official text link", () => {
    const html = renderToStaticMarkup(<BillSummary summary={summary} officialTextUrl={officialTextUrl} />);
    expect(html).toContain("AI-generated");
    expect(html).toContain("It may contain errors.");
    expect(html).toContain('href="/methodology#summaries"');
    expect(html).toContain("Generated September 20, 2026");
    expect(html).toContain(`href="${officialTextUrl}"`);
    expect(html).toContain("Read the official text on Congress.gov");
  });

  it("puts the disclaimer before the summary text", () => {
    const html = renderToStaticMarkup(<BillSummary summary={summary} officialTextUrl={officialTextUrl} />);
    expect(html.indexOf("AI-generated")).toBeLessThan(html.indexOf("Would rename a post office."));
  });

  it("still shows the disclaimer without a date or link", () => {
    const html = renderToStaticMarkup(<BillSummary summary={{ bill_id: "119-hr-42", short_summary: "x" }} />);
    expect(html).toContain("It may contain errors.");
    expect(html).not.toContain("Generated");
    expect(html).not.toContain("congress.gov");
  });

  it("says when the CRS summary was used as context, and only then", () => {
    const withCrs = renderToStaticMarkup(<BillSummary summary={{ ...summary, with_crs_summary: true }} />);
    expect(withCrs).toContain(
      "AI-generated from the official bill text, with the Congressional Research Service summary as context, " +
        "and not reviewed by a person. It may contain errors.",
    );
    const without = renderToStaticMarkup(<BillSummary summary={summary} />);
    expect(without).toContain("AI-generated from the official bill text and not reviewed by a person.");
    expect(without).not.toContain("Congressional Research Service");
  });

  it("names the Federal Register context when a CRA resolution's rule was used (#643)", () => {
    const withRule = renderToStaticMarkup(<BillSummary summary={{ ...summary, with_rule_context: true }} />);
    expect(withRule).toContain(
      "AI-generated from the official bill text, with the Federal Register&#x27;s description of the rule as " +
        "context, and not reviewed by a person. It may contain errors.",
    );
  });

  it("names both contexts when the model had the CRS summary and the rule", () => {
    const both = renderToStaticMarkup(
      <BillSummary summary={{ ...summary, with_crs_summary: true, with_rule_context: true }} />,
    );
    expect(both).toContain(
      "AI-generated from the official bill text, with the Congressional Research Service summary and the Federal " +
        "Register&#x27;s description of the rule as context, and not reviewed by a person.",
    );
  });

  it("doesn't name the Federal Register without the rule context", () => {
    const html = renderToStaticMarkup(<BillSummary summary={{ ...summary, with_rule_context: false }} />);
    expect(html).not.toContain("Federal Register");
  });

  it("shows no disclaimer when there is no summary", () => {
    const html = renderToStaticMarkup(<BillSummary summary={null} officialTextUrl={officialTextUrl} />);
    expect(html).toContain("No AI summary available");
    expect(html).not.toContain("It may contain errors.");
  });

  it('labels the third section "Who it affects"', () => {
    const html = renderToStaticMarkup(
      <BillSummary summary={{ ...summary, who_it_affects: "Post office users." }} />,
    );
    expect(html).toContain("Who it affects");
    expect(html).not.toMatch(/why it matters/i);
    expect(html.split("Post office users.")).toHaveLength(2);
  });

  it("leaves the section out without who_it_affects", () => {
    const html = renderToStaticMarkup(<BillSummary summary={summary} />);
    expect(html).not.toContain("Who it affects");
  });

  it("says which text version and model the summary is based on", () => {
    const html = renderToStaticMarkup(
      <BillSummary
        summary={{
          ...summary,
          model_used: "gemini-3.5-flash",
          source_version_code: "rh",
          source_version_name: "Reported in House",
        }}
      />,
    );
    expect(html).toContain("Based on the <em>Reported in House</em> text · <em>gemini-3.5-flash</em>");
    // The model is named once, in that line.
    expect(html.split("gemini-3.5-flash")).toHaveLength(2);
  });

  it("falls back to the version code when the bill no longer lists the version", () => {
    const html = renderToStaticMarkup(
      <BillSummary summary={{ ...summary, model_used: "gemini-3.5-flash", source_version_code: "rh" }} />,
    );
    expect(html).toContain("Based on the <em>rh</em> text · <em>gemini-3.5-flash</em>");
  });

  it("shows only the model for a summary without a source version", () => {
    const html = renderToStaticMarkup(<BillSummary summary={{ ...summary, model_used: "gemini-2.5-flash" }} />);
    expect(html).toContain("Model: <em>gemini-2.5-flash</em>");
    expect(html).not.toContain("Based on");
  });

  it("shows no provenance line without a version or a model", () => {
    const html = renderToStaticMarkup(<BillSummary summary={summary} />);
    expect(html).not.toContain("Based on");
    expect(html).not.toContain("Model:");
  });
});
