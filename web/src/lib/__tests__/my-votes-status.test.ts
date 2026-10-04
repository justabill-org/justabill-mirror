import { describe, expect, it } from "vitest";
import { myVoteSummary, statusGroup, type BillStatuses } from "@/lib/my-votes";
import type { LocalVotes } from "@/lib/local/votes";

// How My votes groups where a bill stands (#843), from the 119th's list of bills past a chamber.

const STATUSES: BillStatuses = {
  byId: {
    "hr-119-1": "became_law",
    "hr-119-2": "signed",
    "hr-119-3": "passed_house",
    "hr-119-4": "vetoed",
    "hr-119-5": "to_president",
    "hr-119-6": "resolving_differences",
  },
  congresses: new Set([119]),
};

describe("statusGroup", () => {
  it.each([
    ["hr-119-1", "laws"],
    ["hr-119-2", "laws"],
    ["hr-119-3", "passed"],
    ["hr-119-4", "passed"],
    ["hr-119-5", "passed"],
    ["hr-119-6", "passed"],
    // Not on its loaded congress's list: no chamber passed it.
    ["s-119-77", "not_passed"],
    // Its congress's list didn't load: unknown, never guessed.
    ["hr-118-1", "unknown"],
    ["not-a-bill", "unknown"],
  ])("puts %s in %s", (billId, group) => {
    expect(statusGroup(STATUSES, billId)).toBe(group);
  });
});

describe("myVoteSummary", () => {
  const at = "2026-10-01T12:00:00.000Z";

  it("counts the bills that became law, signed ones included", () => {
    const votes: LocalVotes = {
      "hr-119-1": { vote: "yea", at },
      "hr-119-2": { vote: "skip", at },
      "hr-119-3": { vote: "nay", at },
      "s-119-77": { vote: "yea", at },
    };
    expect(myVoteSummary(votes, STATUSES)).toEqual({ total: 4, yea: 2, nay: 1, skip: 1, becameLaw: 2 });
  });

  it("gives no count of laws while any voted-on bill's status is unknown", () => {
    const votes: LocalVotes = { "hr-119-1": { vote: "yea", at }, "hr-118-1": { vote: "nay", at } };
    expect(myVoteSummary(votes, STATUSES).becameLaw).toBeNull();
  });
});
