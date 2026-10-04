// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { DisapprovedRuleCard } from "../bill/disapproved-rule";
import type { DisapprovedRule, FRDocument } from "@/lib/types";
import { axeViolations } from "@/test/axe";

// #643: the card for the rule a CRA resolution disapproves (docs/design/590-cra-disapproved-rules.md, "Web").

afterEach(cleanup);

const ABSTRACT =
  "The Consumer Financial Protection Bureau (CFPB) amends Regulations E and Z to update regulatory exceptions for " +
  "overdraft credit provided by very large financial institutions.";

/** S.J.Res. 18 of the 119th, as GET /bills/sjres-119-18 serves it (Federal Register document 2024-29699). */
const overdraft: FRDocument = {
  document_number: "2024-29699",
  citation: "89 FR 106768",
  type: "Rule",
  action: "Final rule; official interpretation.",
  title: "Overdraft Lending: Very Large Financial Institutions",
  agencies: ["Consumer Financial Protection Bureau"],
  publication_date: "2024-12-30",
  effective_on: "2025-10-01",
  abstract: ABSTRACT,
  html_url: "https://www.federalregister.gov/documents/2024/12/30/2024-29699/overdraft-lending-very-large-financial-institutions",
  pdf_url: "https://www.govinfo.gov/content/pkg/FR-2024-12-30/pdf/2024-29699.pdf",
  docket_id: "CFPB-2024-0002",
};

const SEARCH =
  "https://www.federalregister.gov/documents/search?conditions%5Bterm%5D=" +
  "%22Overdraft+Lending%3A+Very+Large+Financial+Institutions%22+Bureau+of+Consumer+Financial+Protection";

const matched: DisapprovedRule = {
  status: "matched",
  method: "citation",
  rule_title: "Overdraft Lending: Very Large Financial Institutions",
  rule_agency: "Bureau of Consumer Financial Protection",
  cited: "89 Fed. Reg. 106768 (December 30, 2024)",
  gao_opinion: false,
  document: overdraft,
  withdrawn_document: null,
  search_url: SEARCH,
  checked_at: "2026-10-04T06:00:00Z",
};

const unmatched: DisapprovedRule = {
  status: "unmatched",
  reason: "no_candidates",
  rule_title: "Miles City Field Office Record of Decision and Approved Resource Management Plan Amendment",
  rule_agency: "Bureau of Land Management",
  cited: null,
  gao_opinion: true,
  document: null,
  withdrawn_document: null,
  search_url:
    "https://www.federalregister.gov/documents/search?conditions%5Bterm%5D=%22Miles+City+Field+Office%22",
  checked_at: "2026-10-04T06:00:00Z",
};

/** The href of the link with this accessible name. */
const href = (name: string | RegExp) => screen.getByRole("link", { name }).getAttribute("href");

describe("DisapprovedRuleCard, matched by citation (S.J.Res. 18)", () => {
  it("shows the rule's title, agency, kind, dates, citation and abstract, from the Federal Register", () => {
    render(<DisapprovedRuleCard rule={matched} />);
    expect(screen.getByRole("heading", { name: /^The rule this resolution disapproves/ })).toBeTruthy();
    expect(screen.getByText("From the Federal Register")).toBeTruthy();
    expect(href("Overdraft Lending: Very Large Financial Institutions")).toBe(overdraft.html_url);
    expect(screen.getByText("Consumer Financial Protection Bureau")).toBeTruthy();
    expect(
      screen.getByText("Final rule · Published December 30, 2024 · 89 FR 106768 · Effective October 1, 2025"),
    ).toBeTruthy();
    expect(screen.getByText("Abstract, as published by the agency:")).toBeTruthy();
    const quote = document.querySelector("blockquote");
    expect(quote?.textContent).toBe(ABSTRACT);
    expect(screen.queryByText(/AI/)).toBeNull();
  });

  it("links the rule, the official PDF and the docket", () => {
    render(<DisapprovedRuleCard rule={matched} />);
    expect(href("Read the rule on FederalRegister.gov")).toBe(overdraft.html_url);
    expect(href("Official PDF (GovInfo)")).toBe(overdraft.pdf_url);
    expect(href("Docket CFPB-2024-0002 on Regulations.gov")).toBe("https://www.regulations.gov/docket/CFPB-2024-0002");
  });

  it("says what disapproval does under the Congressional Review Act, linking 5 U.S.C. 801", () => {
    render(<DisapprovedRuleCard rule={matched} />);
    expect(href("Congressional Review Act")).toBe(
      "https://uscode.house.gov/view.xhtml?req=granuleid:USC-prelim-title5-section801&num=0&edition=prelim",
    );
    expect(
      screen.getByText(/if this resolution becomes law, the rule has no force or effect, and the agency can't issue/),
    ).toBeTruthy();
  });

  it("says nothing about a title match or a failed match", () => {
    render(<DisapprovedRuleCard rule={matched} />);
    expect(screen.queryByText(/matched this document by its title/)).toBeNull();
    expect(screen.queryByText(/couldn't match/)).toBeNull();
    expect(screen.queryByRole("link", { name: /Search the Federal Register/ })).toBeNull();
  });

  it("leaves out the links the API didn't serve, and a docket ID that isn't one", () => {
    render(
      <DisapprovedRuleCard
        rule={{ ...matched, document: { ...overdraft, html_url: null, pdf_url: null, docket_id: "Docket No. 7" } }}
      />,
    );
    expect(screen.queryAllByRole("link").map((a) => a.textContent)).toEqual(["Congressional Review Act"]);
  });

  it("says when the Federal Register entry has no abstract, and leaves out a missing effective date", () => {
    render(<DisapprovedRuleCard rule={{ ...matched, document: { ...overdraft, abstract: null, effective_on: null } }} />);
    expect(screen.getByText("The Federal Register entry has no abstract.")).toBeTruthy();
    expect(document.querySelector("blockquote")).toBeNull();
    expect(screen.getByText("Final rule · Published December 30, 2024 · 89 FR 106768")).toBeTruthy();
  });

  it("shows a long abstract's start behind Read more, and all of it after", () => {
    const long = `${"word ".repeat(150)}end of the abstract.`;
    render(<DisapprovedRuleCard rule={{ ...matched, document: { ...overdraft, abstract: long } }} />);
    const quote = () => document.querySelector("blockquote")?.textContent ?? "";
    expect(quote()).not.toContain("end of the abstract.");
    expect(quote().endsWith("…")).toBe(true);
    const more = screen.getByRole("button", { name: "Read more" });
    expect(more.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(more);
    expect(quote()).toContain("end of the abstract.");
    expect(screen.getByRole("button", { name: "Show less" }).getAttribute("aria-expanded")).toBe("true");
  });

  it("has no axe violations", async () => {
    const { container } = render(<DisapprovedRuleCard rule={matched} />);
    expect(await axeViolations(container)).toEqual([]);
  });
});

describe("DisapprovedRuleCard, matched by title", () => {
  it("says the resolution cites no page and links how we match", () => {
    render(<DisapprovedRuleCard rule={{ ...matched, method: "title", cited: null }} />);
    const note = screen.getByRole("note");
    expect(note.textContent).toBe(
      "The resolution doesn't cite a Federal Register page. We matched this document by its title, agency and " +
        "date. How we match",
    );
    expect(within(note).getByRole("link", { name: "How we match" }).getAttribute("href")).toBe("/methodology#cra-rules");
  });

  it("names the citation the text has when it led elsewhere", () => {
    render(<DisapprovedRuleCard rule={{ ...matched, method: "title", cited: "89 Fed. Reg. 48517 (June 4, 2024)" }} />);
    expect(screen.getByRole("note").textContent).toBe(
      "We matched this document by its title, agency and date. The resolution's text cites 89 Fed. Reg. 48517 " +
        "(June 4, 2024), a different document. How we match",
    );
  });
});

describe("DisapprovedRuleCard, a withdrawal", () => {
  const withdrawn: FRDocument = {
    ...overdraft,
    document_number: "2022-10533",
    citation: "87 FR 30097",
    type: "Rule",
    action: "Interpretive rule.",
    title: "Revocations or Unfavorable Changes to the Terms of Existing Credit Arrangements",
    publication_date: "2022-05-18",
    effective_on: null,
    abstract: "The Bureau is issuing this interpretive rule.",
    html_url: "https://www.federalregister.gov/documents/2022/05/18/2022-10533/x",
    docket_id: null,
  };

  it("shows the document the disapproved one withdrew", () => {
    render(<DisapprovedRuleCard rule={{ ...matched, withdrawn_document: withdrawn }} />);
    expect(screen.getByText("This document withdrew:")).toBeTruthy();
    expect(href(withdrawn.title)).toBe(withdrawn.html_url);
    expect(screen.getByText("Interpretive rule · Published May 18, 2022 · 87 FR 30097")).toBeTruthy();
    expect(screen.getByText("The Bureau is issuing this interpretive rule.")).toBeTruthy();
  });

  it("has no axe violations", async () => {
    const { container } = render(<DisapprovedRuleCard rule={{ ...matched, withdrawn_document: withdrawn }} />);
    expect(await axeViolations(container)).toEqual([]);
  });
});

describe("DisapprovedRuleCard, unmatched", () => {
  it("says it couldn't be matched, adds the GAO sentence, links a search, and shows no rule", () => {
    render(<DisapprovedRuleCard rule={unmatched} />);
    expect(
      screen.getByText("We couldn't match this resolution to a Federal Register document, so we don't show one."),
    ).toBeTruthy();
    expect(screen.getByText(/by its issue date and a Government Accountability Office opinion that it is a rule/))
      .toBeTruthy();
    expect(
      href(
        "Search the Federal Register for “Miles City Field Office Record of Decision and Approved Resource " +
          "Management Plan Amendment”",
      ),
    ).toBe(unmatched.search_url);
    expect(screen.queryByText("From the Federal Register")).toBeNull();
    expect(document.querySelector("blockquote")).toBeNull();
    expect(screen.queryByRole("link", { name: /Regulations.gov|GovInfo/ })).toBeNull();
  });

  it("leaves out the GAO sentence when the resolution doesn't use a GAO opinion", () => {
    render(<DisapprovedRuleCard rule={{ ...unmatched, gao_opinion: false, reason: "ambiguous" }} />);
    expect(screen.queryByText(/Government Accountability Office/)).toBeNull();
    expect(screen.getByText(/couldn't match this resolution/)).toBeTruthy();
  });

  it("shows no rule even if a document came with an unmatched status", () => {
    render(<DisapprovedRuleCard rule={{ ...unmatched, document: overdraft }} />);
    expect(screen.queryByText(overdraft.title)).toBeNull();
  });

  it("has no axe violations", async () => {
    const { container } = render(<DisapprovedRuleCard rule={unmatched} />);
    expect(await axeViolations(container)).toEqual([]);
  });
});
