import { describe, it, expect } from "vitest";
import { parseBillFilters } from "../bill-filters";
import type { BillListItem, PaginatedResult } from "../types";
import {
  createDeckReader,
  deckListParams,
  forgetOldVoteFilter,
  toDeckCards,
  type DeckItem,
  type DeckReader,
  type DeckSource,
} from "../vote-deck";

function item(n: number, summary: string | null = null): BillListItem {
  return {
    id: `hr-119-${n}`,
    congress: 119,
    bill_type: "hr",
    number: n,
    title: `Bill ${n}`,
    summary: summary ? { bill_id: `hr-119-${n}`, short_summary: summary } : null,
  };
}

const ids = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => `hr-119-${from + i}`);

/** Lets every read the reader started (fakes answer at once) finish. */
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

/**
 * The API's list as the reader sees it: `order` (bill IDs) paged by offset, leaving out the voted
 * ones when `unvoted`. It records each read's offset and the most reads in flight at once.
 */
function fakeList(order: () => string[], { unvoted = false, votes = new Set<string>(), fail = 0 } = {}) {
  const offsets: number[] = [];
  let inFlight = 0;
  let maxInFlight = 0;
  let failures = fail;
  const list = async (offset: number, limit: number): Promise<PaginatedResult<DeckItem>> => {
    offsets.push(offset);
    inFlight++;
    maxInFlight = Math.max(maxInFlight, inFlight);
    await Promise.resolve();
    inFlight--;
    if (failures > 0) {
      failures--;
      throw new Error("429");
    }
    const all = order().filter((id) => !unvoted || !votes.has(id));
    const items = all.slice(offset, offset + limit).map((id) => item(Number(id.split("-")[2])));
    return { items, total: all.length, offset, limit };
  };
  return { list, offsets, votes, maxInFlight: () => maxInFlight };
}

function source(list: DeckSource["list"], votes: Set<string>, extra: Partial<DeckSource> = {}): DeckSource {
  return { list, unvoted: false, isVoted: (id) => votes.has(id), maxOffset: 10_000, ...extra };
}

/** Votes on the current card and deals the next, `n` times, letting reads finish in between. */
async function voteOn(reader: DeckReader, votes: Set<string>, n: number): Promise<string[]> {
  const dealt: string[] = [];
  for (let i = 0; i < n; i++) {
    const card = reader.getSnapshot().card;
    if (!card) break;
    dealt.push(card.bill.id);
    votes.add(card.bill.id);
    reader.next();
    await settle();
  }
  return dealt;
}

describe("toDeckCards", () => {
  it("splits each item into its bill, summary and card facts", () => {
    const [card] = toDeckCards([item(1, "Does a thing.")]);
    expect(card.bill).toEqual({ id: "hr-119-1", congress: 119, bill_type: "hr", number: 1, title: "Bill 1" });
    expect(card.summary?.short_summary).toBe("Does a thing.");
    expect(card.card).toBeNull();
  });

  it("treats a missing summary as null", () => {
    const { summary, ...bill } = item(2);
    expect(summary).toBeNull();
    expect(toDeckCards([bill as BillListItem])[0].summary).toBeNull();
  });
});

describe("deckListParams", () => {
  it("reads the /bills view's statuses as one list, with the other filters", () => {
    expect(deckListParams(parseBillFilters(new URLSearchParams()), 119)).toEqual({
      congress: 119,
      sort: "latest_action",
      status: ["became_law", "signed"],
    });
    expect(deckListParams(parseBillFilters(new URLSearchParams("show=passed&type=hr&area=Health")), 119)).toEqual({
      congress: 119,
      sort: "latest_action",
      type: "hr",
      policy_area: "Health",
      status: ["passed_house", "passed_senate", "resolving_differences", "to_president", "vetoed"],
    });
  });

  it("sends no status for All, and no congress for every congress", () => {
    expect(deckListParams(parseBillFilters(new URLSearchParams("show=all&congress=all&q=water")), 119)).toEqual({
      sort: "latest_action",
      q: "water",
    });
  });
});

describe("createDeckReader", () => {
  it("deals all 120 bills in list order, reading the next 20 when 5 are left, one read at a time", async () => {
    const votes = new Set<string>();
    const api = fakeList(() => ids(1, 120), { votes });
    const reader = createDeckReader(source(api.list, votes));
    expect(reader.getSnapshot().status).toBe("loading");
    reader.start();
    await settle();
    expect(api.offsets).toEqual([0]);
    expect(reader.getSnapshot()).toMatchObject({ status: "ready", total: 120, left: 120 });

    // 15 votes leave 5 cards: the next batch is read before they run out.
    await voteOn(reader, votes, 14);
    expect(api.offsets).toEqual([0]);
    await voteOn(reader, votes, 1);
    expect(api.offsets).toEqual([0, 20]);
    expect(reader.getSnapshot()).toMatchObject({ status: "ready", left: 105 });

    const rest = await voteOn(reader, votes, 200);
    expect(rest).toEqual(ids(16, 120));
    expect(api.offsets).toEqual([0, 20, 40, 60, 80, 100]);
    expect(api.maxInFlight()).toBe(1);
    expect(reader.getSnapshot()).toMatchObject({ status: "end", card: null, total: 120, left: 0 });
  });

  it("reads on past 100 bills this device voted on, one read at a time, loading meanwhile", async () => {
    const votes = new Set(ids(1, 100));
    const api = fakeList(() => ids(1, 130));
    const reader = createDeckReader(source(api.list, votes));
    const seen: string[] = [];
    reader.subscribe(() => seen.push(reader.getSnapshot().status));
    reader.start();
    await settle();

    expect(api.offsets).toEqual([0, 20, 40, 60, 80, 100]);
    expect(api.maxInFlight()).toBe(1);
    expect(seen.slice(0, -1).every((s) => s === "loading")).toBe(true);
    expect(reader.getSnapshot()).toMatchObject({ status: "ready", total: 130, left: 30 });
    expect(reader.getSnapshot().card?.bill.id).toBe("hr-119-101");
  });

  it("says the end, not 'nothing found', when every bill was voted on before", async () => {
    const votes = new Set(ids(1, 30));
    const api = fakeList(() => ids(1, 30));
    const reader = createDeckReader(source(api.list, votes));
    reader.start();
    await settle();
    expect(reader.getSnapshot()).toMatchObject({ status: "end", total: 30, left: 0 });
  });

  it("is empty when the filters match no bill", async () => {
    const api = fakeList(() => []);
    const reader = createDeckReader(source(api.list, new Set()));
    reader.start();
    await settle();
    expect(reader.getSnapshot()).toMatchObject({ status: "empty", total: 0 });
  });

  it("never deals a bill twice when the list reorders between batches", async () => {
    const votes = new Set<string>();
    let order = ids(1, 40);
    const api = fakeList(() => order, { votes });
    const reader = createDeckReader(source(api.list, votes));
    reader.start();
    await settle();
    // A sync moves bills 3 and 5 below the first batch, pushing 19 and 20 into the second.
    order = [...ids(1, 2), "hr-119-4", ...ids(6, 22), "hr-119-3", "hr-119-5", ...ids(23, 40)];

    const dealt = await voteOn(reader, votes, 100);
    expect(dealt).toHaveLength(new Set(dealt).size);
    // 21 and 22 moved up into the part already read: this visit misses them (the next one deals them).
    expect(new Set(dealt)).toEqual(new Set(ids(1, 40).filter((id) => id !== "hr-119-21" && id !== "hr-119-22")));
    expect(reader.getSnapshot().status).toBe("end");
  });

  it("keeps its place when a batch fails, and Try again reads that batch", async () => {
    const votes = new Set<string>();
    const errors: unknown[] = [];
    const api = fakeList(() => ids(1, 30), { votes });
    let failNext = false;
    const list: DeckSource["list"] = (offset, limit) => {
      if (failNext) {
        failNext = false;
        return Promise.reject(new Error("offline"));
      }
      return api.list(offset, limit);
    };
    const reader = createDeckReader(source(list, votes, { onError: (err) => errors.push(err) }));
    reader.start();
    await settle();
    failNext = true;

    // The read at 5 left fails; the 5 cards still deal, then the deck says so.
    expect(await voteOn(reader, votes, 20)).toEqual(ids(1, 20));
    expect(reader.getSnapshot()).toMatchObject({ status: "failed", card: null, left: 10 });
    expect(errors).toHaveLength(1);

    reader.retry();
    await settle();
    expect(api.offsets).toEqual([0, 20]);
    expect(reader.getSnapshot().card?.bill.id).toBe("hr-119-21");
  });

  it("stops at the API's deepest page and says so, rather than claiming the end", async () => {
    const votes = new Set<string>();
    const api = fakeList(() => ids(1, 100), { votes });
    const reader = createDeckReader(source(api.list, votes, { maxOffset: 40 }));
    reader.start();
    await settle();

    expect(await voteOn(reader, votes, 100)).toEqual(ids(1, 60));
    expect(api.offsets).toEqual([0, 20, 40]);
    expect(reader.getSnapshot()).toMatchObject({ status: "capped", card: null });
  });

  it("deals the rendered first batch without reading it again", async () => {
    const votes = new Set(["hr-119-1"]);
    const api = fakeList(() => ids(1, 3));
    const first = { items: [1, 2, 3].map((n) => item(n)), total: 3, offset: 0, limit: 20 };
    const reader = createDeckReader(source(api.list, votes), { first });
    expect(reader.getSnapshot()).toMatchObject({ status: "ready", total: 3, left: 2 });
    expect(reader.getSnapshot().card?.bill.id).toBe("hr-119-2");
    reader.start();
    await settle();
    expect(api.offsets).toEqual([]);
  });

  describe("signed in (unvoted batches)", () => {
    it("starts each batch earlier by the bills voted on since, so none is skipped", async () => {
      const votes = new Set(["hr-119-2"]);
      const api = fakeList(() => ids(1, 50), { unvoted: true, votes });
      let counts = 0;
      const count = async () => {
        counts++;
        return 50;
      };
      const reader = createDeckReader(source(api.list, votes, { unvoted: true, count }));
      reader.start();
      await settle();
      expect(reader.getSnapshot()).toMatchObject({ status: "ready", total: 50, left: 49 });

      const dealt = await voteOn(reader, votes, 100);
      expect(dealt).toEqual(ids(1, 50).filter((id) => id !== "hr-119-2"));
      // 15 of the first 20 were voted on when the second batch was read.
      expect(api.offsets.slice(0, 2)).toEqual([0, 5]);
      expect(counts).toBe(1);
      expect(reader.getSnapshot()).toMatchObject({ status: "end", left: 0 });
    });

    it("takes the total from the rendered batch but reads its own list from the start", async () => {
      const votes = new Set<string>();
      const api = fakeList(() => ids(1, 3), { unvoted: true, votes });
      const count = () => Promise.reject(new Error("not needed"));
      const first = { items: [item(9)], total: 7, offset: 0, limit: 20 };
      const reader = createDeckReader(source(api.list, votes, { unvoted: true, count }), { first });
      reader.start();
      await settle();
      expect(api.offsets).toEqual([0]);
      expect(reader.getSnapshot()).toMatchObject({ status: "ready", total: 7, left: 3 });
      expect(reader.getSnapshot().card?.bill.id).toBe("hr-119-1");
    });

    it("is empty only when the list itself is, not when every bill in it is voted on", async () => {
      const votes = new Set(ids(1, 3));
      const api = fakeList(() => ids(1, 3), { unvoted: true, votes });
      const reader = createDeckReader(source(api.list, votes, { unvoted: true, count: async () => 3 }));
      reader.start();
      await settle();
      expect(reader.getSnapshot()).toMatchObject({ status: "end", total: 3 });
    });
  });

  it("drops a batch that arrives after the deck was stopped", async () => {
    const api = fakeList(() => ids(1, 3));
    const reader = createDeckReader(source(api.list, new Set()));
    reader.start();
    reader.stop();
    await settle();
    expect(reader.getSnapshot()).toMatchObject({ status: "loading", card: null });
  });
});

describe("forgetOldVoteFilter", () => {
  it("removes the filter older versions saved, and ignores storage that throws", () => {
    const data = new Map([
      ["jab.vote-filter.v1", '{"stage":"law","topic":null}'],
      ["jab.vote-filter.corrupt", "x"],
      ["jab.votes.v1", "{}"],
    ]);
    forgetOldVoteFilter(() => ({ removeItem: (k) => void data.delete(k) }));
    expect([...data.keys()]).toEqual(["jab.votes.v1"]);

    expect(() =>
      forgetOldVoteFilter(() => ({
        removeItem: () => {
          throw new Error("SecurityError");
        },
      }))
    ).not.toThrow();
    expect(() => forgetOldVoteFilter(() => null)).not.toThrow();
    expect(() =>
      forgetOldVoteFilter(() => {
        throw new DOMException("Access is denied for this document.", "SecurityError");
      })
    ).not.toThrow();
  });
});
