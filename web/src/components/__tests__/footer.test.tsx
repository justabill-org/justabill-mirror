// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { cleanup, render, screen } from "@testing-library/react";
import { axeViolations } from "@/test/axe";
import { Footer } from "../layout/footer";

afterEach(cleanup);

describe("Footer", () => {
  it("links to every trust page", () => {
    const html = renderToStaticMarkup(<Footer />);
    for (const href of ["/about", "/methodology", "/privacy", "/terms", "/contact"]) {
      expect(html).toContain(`href="${href}"`);
    }
  });

  // #822: the public repo is the last of the About links, right after Contact, named in words.
  it("ends the About links with GitHub, linking to the public repo", () => {
    render(<Footer />);
    const nav = screen.getByRole("navigation", { name: "About Just a Bill" });
    const links = [...nav.querySelectorAll("a")].map((a) => [a.textContent, a.getAttribute("href")]);
    expect(links).toEqual([
      ["About", "/about"],
      ["Methodology", "/methodology"],
      ["Privacy", "/privacy"],
      ["Terms", "/terms"],
      ["Contact", "/contact"],
      ["GitHub", "https://github.com/justabill-org/justabill-mirror"],
    ]);
  });

  // #717: the sources line used to be hidden below sm; now it names every source on every width.
  it("names each data source on every width, linking to its publisher", () => {
    const { container } = render(<Footer />);
    const sources = {
      "Congress.gov": "https://www.congress.gov",
      GovInfo: "https://www.govinfo.gov",
      "the House Clerk": "https://clerk.house.gov",
      "the Senate": "https://www.senate.gov",
    };
    for (const [name, href] of Object.entries(sources)) {
      expect(screen.getByRole("link", { name }).getAttribute("href")).toBe(href);
    }
    const line = screen.getByText(/A nonpartisan project/);
    expect(line.textContent).toBe(
      "A nonpartisan project. Bills and members from Congress.gov, bill text from GovInfo, and " +
        "roll-call votes from the House Clerk and the Senate. AI summaries are labeled as AI.",
    );
    // Nothing in the footer is hidden at some width.
    expect(container.innerHTML).not.toMatch(/\bhidden\b/);
  });

  it("doesn't repeat the main navigation the header has", () => {
    render(<Footer />);
    expect(screen.getAllByRole("navigation").map((n) => n.getAttribute("aria-label"))).toEqual(["About Just a Bill"]);
    expect(screen.queryByRole("link", { name: "Bills" })).toBeNull();
  });

  it("has no axe violations", async () => {
    const { container } = render(<Footer />);
    expect(await axeViolations(container)).toEqual([]);
  });
});
