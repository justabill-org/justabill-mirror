import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { RelatedBills } from "../bill/related-bills";
import { CompanionVotes } from "../bill/companion-votes";
import { WorksMostWith } from "../member/works-most-with";
import type { Collaborator, CompanionRollCall, GraphRelatedBill } from "@/lib/types";

const related: GraphRelatedBill[] = [
  {
    bill_id: "s-119-1",
    congress: 119,
    bill_type: "s",
    number: 1,
    title: "Senate companion",
    current_status: "passed_senate",
    relation_types: ["Identical bill"],
    shared_subjects: 2,
  },
  {
    bill_id: "hr-119-42",
    congress: 119,
    bill_type: "hr",
    number: 42,
    title: "Shares one subject",
    relation_types: [],
    shared_subjects: 1,
  },
];

describe("RelatedBills", () => {
  it("renders nothing without related bills", () => {
    expect(renderToStaticMarkup(<RelatedBills bills={[]} />)).toBe("");
  });

  it("links each bill and shows why it is related", () => {
    const html = renderToStaticMarkup(<RelatedBills bills={related} />);
    expect(html).toContain('href="/bills/s-119-1"');
    expect(html).toContain('href="/bills/hr-119-42"');
    expect(html).toContain("S. 1");
    expect(html).toContain("119th Congress");
    expect(html).toContain("Identical bill");
    expect(html).toContain("2 shared subjects");
    expect(html).toContain("1 shared subject<");
    expect(html).toContain("Passed Senate");
  });
});

const collaborators: Collaborator[] = [
  { bioguide_id: "B000002", first_name: "Bea", last_name: "Bee", party: "R", state: "TX", chamber: "House", shared_bills: 8 },
  { bioguide_id: "C000003", first_name: "Cy", last_name: "Sea", party: "D", state: "OH", chamber: "Senate", shared_bills: 1 },
  { bioguide_id: "D000004", first_name: "Di", last_name: "Dee", shared_bills: 2 },
];

describe("WorksMostWith", () => {
  it("lists collaborators with counts and party-coloured bars", () => {
    const html = renderToStaticMarkup(
      <WorksMostWith memberName="Ann Aye" congress={119} collaborators={collaborators} />
    );
    expect(html).toContain("with Ann Aye in the 119th");
    expect(html).toContain('href="/members/B000002"');
    expect(html).toContain("Bea Bee");
    expect(html).toContain("TX · House · 8 bills");
    expect(html).toContain("OH · Senate · 1 bill<");
    // Bars scale to the top collaborator and take the party colour.
    expect(html).toMatch(/bg-party-r" style="width:100%"/);
    expect(html).toMatch(/bg-party-d" style="width:12.5%"/);
    // Missing party and term fields still render.
    expect(html).toContain("Di Dee");
    expect(html).toMatch(/bg-muted-foreground" style="width:25%"/);
  });

  it("says so when there are no shared bills", () => {
    const html = renderToStaticMarkup(<WorksMostWith memberName="Ann Aye" congress={118} collaborators={[]} />);
    expect(html).toContain("No shared bills recorded for the 118th Congress.");
  });
});

const rollCalls: CompanionRollCall[] = [
  {
    companion_bill_id: "s-119-1",
    vote_id: "senate-119-s1-vote00002",
    chamber: "Senate",
    vote_date: "2025-04-01T17:00:00Z",
    question: "On Passage of the Bill",
    result: "Bill Passed",
    votes: [
      { member_id: "C000003", first_name: "Cora", last_name: "Chen", party: "R", vote: "Yea" },
      { member_id: "A000001", first_name: "Al", last_name: "Aye", party: "D", vote: "Nay" },
      { member_id: "D000004", first_name: "Di", last_name: "Dee", vote: "Not Voting" },
    ],
  },
];

describe("CompanionVotes", () => {
  it("renders nothing without companion roll calls", () => {
    expect(renderToStaticMarkup(<CompanionVotes rollCalls={[]} />)).toBe("");
  });

  it("shows each roll call with a party tally and the member list", () => {
    const html = renderToStaticMarkup(<CompanionVotes rollCalls={rollCalls} />);
    expect(html).toContain("How the other chamber voted");
    expect(html).toContain('href="/bills/s-119-1"');
    expect(html).toContain("S. 1");
    // The bill number is ink with tabular numerals, not amber monospace (#764).
    const number = /<a [^>]*href="\/bills\/s-119-1"[^>]*>/.exec(html)?.[0] ?? "";
    expect(number).toMatch(/class="[^"]*\btabular-nums\b[^"]*\btext-foreground\b/);
    expect(number).not.toMatch(/font-mono|text-link/);
    expect(html).toContain("Senate · April 1, 2025");
    expect(html).toContain("On Passage of the Bill");
    expect(html).toContain("Bill Passed");
    expect(html).toContain("Democrat");
    expect(html).toContain("Republican");
    expect(html).toContain("Party unknown");
    expect(html).not.toContain(">Other<");
    expect(html).toContain("How each member voted (3)");
    // Members sorted by last name, linked to their pages.
    expect(html.indexOf("Al Aye")).toBeLessThan(html.indexOf("Cora Chen"));
    expect(html).toContain('href="/members/C000003"');
  });
});
