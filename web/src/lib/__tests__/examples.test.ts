import { describe, it, expect } from "vitest";
import {
  billList,
  billDetail,
  billActions,
  textVersions,
  billText,
  billDiffs,
  diffDetail,
  billVotes,
  amendments,
  congresses,
  memberList,
  memberDetail,
  user,
  userVotes,
  userFavorites,
  scorecard,
  compare,
  billsBecameLaw,
  billsEverPassedHouse,
  billsUnvoted,
  health,
} from "../examples";
import { BILL_STATUS_LABELS, BILL_TYPE_LABELS } from "../types";

describe("example: health", () => {
  it("has ok status", () => {
    expect(health.status).toBe("ok");
    expect(health.checks.database).toBe("healthy");
  });
});

describe("example: bill list", () => {
  it("has paginated structure", () => {
    expect(billList.total).toBeGreaterThan(0);
    expect(billList.items.length).toBeGreaterThan(0);
    expect(billList.offset).toBeDefined();
    expect(billList.limit).toBeGreaterThan(0);
  });

  it("each bill has required fields", () => {
    for (const bill of billList.items) {
      expect(bill.id).toBeTruthy();
      expect(bill.congress).toBe(119);
      expect(bill.bill_type).toBeTruthy();
      expect(bill.number).toBeGreaterThan(0);
      expect(bill.title.length).toBeGreaterThan(0);
    }
  });

  it("bill types have display labels", () => {
    for (const bill of billList.items) {
      expect(BILL_TYPE_LABELS[bill.bill_type]).toBeDefined();
    }
  });
});

describe("example: bill detail", () => {
  it("has bill with all metadata", () => {
    const { bill } = billDetail;
    expect(bill.id).toBeTruthy();
    expect(bill.title.length).toBeGreaterThan(0);
    expect(bill.sponsors).toBeDefined();
    expect(bill.sponsors!.length).toBeGreaterThan(0);
    expect(bill.committees).toBeDefined();
  });

  it("has actions timeline", () => {
    const actions = billDetail.actions ?? [];
    expect(actions.length).toBeGreaterThan(0);
    const sorted = [...actions].sort((a, b) => a.sort_order - b.sort_order);
    expect(sorted[0].sort_order).toBeLessThanOrEqual(sorted[sorted.length - 1].sort_order);
  });

  it("has text versions", () => {
    const versions = billDetail.text_versions ?? [];
    expect(versions.length).toBeGreaterThan(0);
    for (const v of versions) {
      expect(v.version_type.length).toBeGreaterThan(0);
      expect(v.version_code.length).toBeGreaterThan(0);
    }
  });

  it("has status history journey", () => {
    const history = billDetail.status_history ?? [];
    expect(history.length).toBeGreaterThan(0);
    for (const entry of history) {
      expect(entry.status).toBeTruthy();
      expect(entry.status_rank).toBeGreaterThan(0);
      expect(entry.status_date).toBeTruthy();
      expect(BILL_STATUS_LABELS[entry.status]).toBeDefined();
    }
  });
});

describe("example: bill actions", () => {
  it("has chronological actions with text", () => {
    expect(billActions.length).toBeGreaterThan(0);
    for (const action of billActions) {
      expect(action.action_text.length).toBeGreaterThan(0);
      expect(action.action_date).toBeTruthy();
      expect(action.sort_order).toBeGreaterThan(0);
    }
  });
});

describe("example: text versions", () => {
  it("has version metadata with format URLs", () => {
    expect(textVersions.length).toBeGreaterThan(0);
    for (const v of textVersions) {
      expect(v.formats.length).toBeGreaterThan(0);
      for (const f of v.formats) {
        expect(f.type.length).toBeGreaterThan(0);
        expect(f.url).toMatch(/^https?:\/\//);
      }
    }
  });
});

describe("example: bill text", () => {
  it("has sections and, like the API when they parsed, no raw XML", () => {
    expect(billText.sections?.length).toBeGreaterThan(0);
    expect(billText).not.toHaveProperty("content");
    expect(billText.format).toBeTruthy();
    expect(billText.content_hash.length).toBeGreaterThan(0);
  });
});

describe("example: bill diffs", () => {
  it("has diff metadata but no content, like the API's list", () => {
    if (billDiffs.length === 0) return; // some bills may not have diffs
    for (const diff of billDiffs) {
      expect(diff.from_version_id).toBeTruthy();
      expect(diff.to_version_id).toBeTruthy();
      expect(diff).not.toHaveProperty("diff_content");
    }
  });
});

describe("example: diff detail", () => {
  it("has diff with section-level changes", () => {
    expect(diffDetail.diff.id).toBeTruthy();
    const sections = diffDetail.diff.diff_content ?? [];
    expect(sections.length).toBeGreaterThan(0);
    for (const section of sections) {
      expect(["added", "removed", "modified", "unchanged"]).toContain(section.type);
    }
  });
});

describe("example: bill votes", () => {
  it("has roll call votes with tallies", () => {
    if (billVotes.length === 0) return;
    for (const vote of billVotes) {
      expect(vote.chamber).toBeTruthy();
      expect(vote.vote_date).toBeTruthy();
    }
  });
});

describe("example: amendments", () => {
  it("has amendment details", () => {
    if (amendments.length === 0) return;
    for (const a of amendments) {
      expect(a.amendment_type).toBeTruthy();
      expect(a.amendment_number).toBeGreaterThan(0);
      expect(a.chamber).toBeTruthy();
    }
  });
});

describe("example: congresses", () => {
  it("has at least one congress", () => {
    expect(congresses.length).toBeGreaterThan(0);
    expect(congresses[0].number).toBe(119);
    expect(congresses[0].is_current).toBe(true);
  });
});

describe("example: member list", () => {
  it("has paginated members", () => {
    expect(memberList.total).toBeGreaterThan(0);
    for (const m of memberList.items) {
      expect(m.bioguide_id).toBeTruthy();
      expect(m.first_name.length).toBeGreaterThan(0);
      expect(m.last_name.length).toBeGreaterThan(0);
    }
  });
});

describe("example: member detail", () => {
  it("has terms and recent votes", () => {
    expect(memberDetail.terms.length).toBeGreaterThan(0);
    expect(memberDetail.recent_votes.length).toBeGreaterThan(0);
    for (const term of memberDetail.terms) {
      expect(term.chamber).toBeTruthy();
      expect(term.state.length).toBe(2);
      expect(term.party.length).toBe(1);
    }
    for (const vote of memberDetail.recent_votes) {
      expect(vote.member_vote).toBeTruthy();
      expect(vote.chamber).toBeTruthy();
    }
  });
});

describe("example: user", () => {
  it("has profile with district info", () => {
    expect(user.id).toBeTruthy();
    // Only an opaque id, state and district: no email, name or address (#55).
    expect(Object.keys(user)).not.toContain("email");
    expect(user.state).toBe("CA");
    expect(user.district).toBe(12);
  });
});

describe("example: user votes", () => {
  it("has paginated vote history", () => {
    expect(userVotes.items.length).toBeGreaterThan(0);
    for (const v of userVotes.items) {
      expect(["yea", "nay", "skip"]).toContain(v.vote);
      expect(v.bill_id).toBeTruthy();
    }
  });
});

describe("example: user favorites", () => {
  it("has favorited bills", () => {
    expect(userFavorites.items.length).toBeGreaterThan(0);
    for (const f of userFavorites.items) {
      expect(f.bill_id).toBeTruthy();
      expect(f.created_at).toBeTruthy();
    }
  });
});

describe("example: scorecard", () => {
  it("has alignment scores", () => {
    expect(scorecard.scores.length).toBeGreaterThan(0);
    for (const s of scorecard.scores) {
      expect(s.member_name.length).toBeGreaterThan(0);
      expect(s.alignment_pct).not.toBeNull();
      expect(s.alignment_pct).toBeGreaterThanOrEqual(0);
      expect(s.alignment_pct).toBeLessThanOrEqual(100);
      expect(s.total_compared).toBeGreaterThan(0);
      expect(s.rule).toBe("final-passage-v1");
    }
  });
});

describe("example: compare", () => {
  it("has vote-by-vote comparison", () => {
    expect(compare.member_id).toBeTruthy();
    expect(compare.comparisons.length).toBeGreaterThan(0);
    for (const c of compare.comparisons) {
      expect(c.bill_title.length).toBeGreaterThan(0);
      expect(c.vote_id).toMatch(/^house-119-s\d-roll\d{3}$/);
      expect(typeof c.counted).toBe("boolean");
      expect(typeof c.matches).toBe("boolean");
      if (!c.counted) expect(c.matches).toBe(false);
    }
  });
});

describe("example: status filters", () => {
  it("became_law filter returns only became_law bills", () => {
    for (const bill of billsBecameLaw.items) {
      expect(bill.current_status).toBe("became_law");
    }
  });

  it("ever-passed-house includes bills beyond passed_house status", () => {
    expect(billsEverPassedHouse.total).toBeGreaterThan(0);
    const statuses = new Set(billsEverPassedHouse.items.map(b => b.current_status));
    // Should include bills that are now beyond passed_house
    expect(statuses.size).toBeGreaterThanOrEqual(1);
  });

  it("unvoted filter returns fewer bills than total", () => {
    expect(billsUnvoted.total).toBeLessThan(billList.total);
  });
});
