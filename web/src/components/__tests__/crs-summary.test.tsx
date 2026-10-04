// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { CrsSummary } from "../bill/crs-summary";
import type { CrsSummary as CrsSummaryType } from "@/lib/types";
import { axeViolations } from "@/test/axe";

afterEach(cleanup);

const summaryUrl = "https://www.congress.gov/bill/119th-congress/senate-bill/5/summary/00";
const crs: CrsSummaryType = {
  bill_id: "s-119-5",
  version_code: "00",
  action_date: "2025-06-05T00:00:00Z",
  action_desc: "Introduced in Senate",
  text: "This bill does a thing.\n\n• First item",
  updated_at: "2026-09-28T12:47:20Z",
};

describe("CrsSummary", () => {
  it("names CRS, the version and date it describes, and links it on Congress.gov", () => {
    const html = renderToStaticMarkup(<CrsSummary summary={crs} summaryUrl={summaryUrl} />);
    expect(html).toContain("Official summary");
    expect(html).toContain("Congressional Research Service");
    expect(html).toContain("Summary of the bill as <em>Introduced in Senate</em> · June 5, 2025");
    expect(html).toContain(`href="${summaryUrl}"`);
    expect(html).toContain("Read this summary on Congress.gov");
    expect(html).toContain("whitespace-pre-wrap");
    expect(html).toContain("This bill does a thing.\n\n• First item");
    expect(html).not.toContain("Read more");
    expect(html).not.toContain("changed since");
  });

  it("says when the bill has changed since", () => {
    const html = renderToStaticMarkup(<CrsSummary summary={crs} changedSince />);
    expect(html).toContain("The bill has changed since this summary was written.");
  });

  it("renders markup in the text as text", () => {
    const text = '<script>alert(1)</script>\n\n<b onclick="x()">bold</b>';
    const html = renderToStaticMarkup(<CrsSummary summary={{ ...crs, text }} />);
    expect(html).not.toContain("<script>");
    expect(html).not.toContain("<b ");
    expect(html).toContain("&lt;script&gt;alert(1)&lt;/script&gt;");
  });

  it("collapses a long summary at a paragraph break and expands it", () => {
    const first = "A".repeat(500);
    const rest = "B".repeat(300);
    render(<CrsSummary summary={{ ...crs, text: `${first}\n\n${rest}` }} />);
    expect(screen.queryByText(rest, { exact: false })).toBeNull();

    const button = screen.getByRole("button", { name: "Read more" });
    expect(button.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(button);
    expect(screen.getByText(rest, { exact: false })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Show less" }).getAttribute("aria-expanded")).toBe("true");
  });

  it("has no axe violations, expanded or not", async () => {
    const text = `${"A".repeat(500)}\n\n${"B".repeat(300)}`;
    const { container } = render(<CrsSummary summary={{ ...crs, text }} summaryUrl={summaryUrl} changedSince />);
    expect(await axeViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Read more" }));
    expect(await axeViolations(container)).toEqual([]);
  });
});
