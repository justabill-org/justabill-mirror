import { describe, it, expect } from "vitest";
import {
  BILL_TYPE_LABELS,
  BILL_STATUS_LABELS,
  BILL_STATUS_ORDER,
} from "../types";
import type {
  Bill,
  BillStatus,
  BillType,
  Member,
  MemberDetail,
  User,
  RepScore,
  VoteComparison,
  BillStatusEntry,
  PaginatedResult,
  DiffStats,
  SectionDiff,
  BillTextSection,
} from "../types";

describe("BILL_TYPE_LABELS", () => {
  it("maps all bill types to display labels", () => {
    const expected: Record<string, string> = {
      hr: "H.R.",
      s: "S.",
      hjres: "H.J.Res.",
      sjres: "S.J.Res.",
      hconres: "H.Con.Res.",
      sconres: "S.Con.Res.",
      hres: "H.Res.",
      sres: "S.Res.",
    };
    expect(BILL_TYPE_LABELS).toEqual(expected);
  });

  it("returns a label for every BillType", () => {
    const types: BillType[] = ["hr", "s", "hjres", "sjres", "hconres", "sconres", "hres", "sres"];
    for (const t of types) {
      expect(BILL_TYPE_LABELS[t]).toBeDefined();
      expect(BILL_TYPE_LABELS[t].length).toBeGreaterThan(0);
    }
  });
});

describe("BILL_STATUS_LABELS", () => {
  it("maps all statuses to human-readable labels", () => {
    const statuses: BillStatus[] = [
      "introduced", "in_committee", "reported", "passed_house",
      "passed_senate", "resolving_differences", "to_president",
      "signed", "vetoed", "became_law",
    ];
    for (const s of statuses) {
      expect(BILL_STATUS_LABELS[s]).toBeDefined();
      expect(BILL_STATUS_LABELS[s].length).toBeGreaterThan(0);
    }
  });
});

describe("BILL_STATUS_ORDER", () => {
  it("has the correct lifecycle progression", () => {
    expect(BILL_STATUS_ORDER[0]).toBe("introduced");
    expect(BILL_STATUS_ORDER[BILL_STATUS_ORDER.length - 1]).toBe("became_law");
  });

  it("has passed_house before passed_senate", () => {
    const houseIdx = BILL_STATUS_ORDER.indexOf("passed_house");
    const senateIdx = BILL_STATUS_ORDER.indexOf("passed_senate");
    expect(houseIdx).toBeLessThan(senateIdx);
  });

  it("contains all statuses except vetoed", () => {
    // Vetoed is a branch, not part of the linear progression
    expect(BILL_STATUS_ORDER).not.toContain("vetoed");
    expect(BILL_STATUS_ORDER.length).toBe(9);
  });
});

describe("type shape validation with example data", () => {
  it("Bill has required fields", () => {
    const bill: Bill = {
      id: "hr-119-1",
      congress: 119,
      bill_type: "hr",
      number: 1,
      title: "Test Bill",
    };
    expect(bill.id).toBe("hr-119-1");
    expect(bill.congress).toBe(119);
  });

  it("Bill optional fields can be omitted", () => {
    const bill: Bill = {
      id: "hr-119-1",
      congress: 119,
      bill_type: "hr",
      number: 1,
      title: "Test Bill",
    };
    expect(bill.introduced_date).toBeUndefined();
    expect(bill.sponsors).toBeUndefined();
    expect(bill.current_status).toBeUndefined();
  });

  it("Member has required fields", () => {
    const member: Member = {
      bioguide_id: "P000197",
      first_name: "Nancy",
      last_name: "Pelosi",
    };
    expect(member.bioguide_id).toBe("P000197");
  });

  it("MemberDetail extends Member with terms and votes", () => {
    const detail: MemberDetail = {
      bioguide_id: "P000197",
      first_name: "Nancy",
      last_name: "Pelosi",
      terms: [
        { member_id: "P000197", congress: 119, chamber: "House", state: "CA", district: 11, party: "D" },
      ],
      recent_votes: [
        { vote_id: "house-119-roll001", vote_date: "2025-01-15T00:00:00Z", member_vote: "Yea", chamber: "House" },
      ],
    };
    expect(detail.terms).toHaveLength(1);
    expect(detail.recent_votes).toHaveLength(1);
  });

  it("User has required fields", () => {
    const user: User = {
      id: "uuid-123",
      created_at: "2025-01-01T00:00:00Z",
    };
    expect(user.id).toBe("uuid-123");
    expect(user.state).toBeUndefined();
  });

  it("RepScore has alignment percentage", () => {
    const score: RepScore = {
      member_id: "P000197",
      member_name: "Nancy Pelosi",
      chamber: "House",
      party: "D",
      matching_votes: 8,
      total_compared: 10,
      member_absent: 1,
      alignment_pct: 80.0,
      rule: "final-passage-v1",
    };
    expect(score.alignment_pct).toBe(80.0);
  });

  it("RepScore has a null percentage when nothing is compared", () => {
    const score: RepScore = {
      member_id: "C000003",
      member_name: "Cora Chen",
      chamber: "Senate",
      party: "R",
      matching_votes: 0,
      total_compared: 0,
      member_absent: 2,
      alignment_pct: null,
      rule: "final-passage-v1",
    };
    expect(score.alignment_pct).toBeNull();
  });

  it("VoteComparison shows match status", () => {
    const comp: VoteComparison = {
      bill_id: "hr-119-1",
      bill_title: "Test Bill",
      vote_id: "house-119-s1-roll001",
      vote_date: "2025-03-04T17:00:00Z",
      question: "On Passage",
      user_vote: "yea",
      member_vote: "yea",
      counted: true,
      matches: true,
    };
    expect(comp.matches).toBe(true);
  });

  it("BillStatusEntry has rank ordering", () => {
    const entries: BillStatusEntry[] = [
      { status: "introduced", status_date: "2025-01-03", status_rank: 1 },
      { status: "in_committee", status_date: "2025-01-03", status_rank: 2 },
      { status: "became_law", status_date: "2025-12-26", status_rank: 10 },
    ];
    const sorted = [...entries].sort((a, b) => a.status_rank - b.status_rank);
    expect(sorted[0].status).toBe("introduced");
    expect(sorted[sorted.length - 1].status).toBe("became_law");
  });

  it("PaginatedResult wraps items with metadata", () => {
    const result: PaginatedResult<Bill> = {
      items: [{ id: "hr-119-1", congress: 119, bill_type: "hr", number: 1, title: "Test" }],
      total: 100,
      offset: 0,
      limit: 20,
    };
    expect(result.items).toHaveLength(1);
    expect(result.total).toBe(100);
  });

  it("DiffStats tracks section and word changes", () => {
    const stats: DiffStats = {
      sections_added: 3,
      sections_removed: 1,
      sections_modified: 5,
      words_added: 420,
      words_removed: 88,
    };
    expect(stats.sections_added + stats.sections_removed + stats.sections_modified).toBe(9);
  });

  it("SectionDiff has typed change categories", () => {
    const diffs: SectionDiff[] = [
      { section_id: "sec-1", header: "Section 1", type: "added", new_text: "New content" },
      { section_id: "sec-2", header: "Section 2", type: "removed", old_text: "Old content" },
      { section_id: "sec-3", header: "Section 3", type: "modified", old_text: "Before", new_text: "After" },
      { section_id: "sec-4", header: "Section 4", type: "unchanged" },
    ];
    expect(diffs.filter(d => d.type === "added")).toHaveLength(1);
    expect(diffs.filter(d => d.type === "unchanged")).toHaveLength(1);
  });

  it("BillTextSection supports nested children", () => {
    const section: BillTextSection = {
      id: "sec-1",
      header: "Title I",
      content: "",
      children: [
        { id: "sec-1-a", header: "Section 101", content: "Text here" },
        { id: "sec-1-b", header: "Section 102", content: "More text" },
      ],
    };
    expect(section.children).toHaveLength(2);
    expect(section.children![0].header).toBe("Section 101");
  });
});
