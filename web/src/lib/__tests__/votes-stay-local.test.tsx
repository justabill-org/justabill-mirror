import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { billList } from "../examples";
import { createRepsStore } from "../local/reps";
import type { LocalEnv } from "../local/storage";
import { createVoteStore, type LocalVotes } from "../local/votes";
import { congressesVotedIn, loadPositions, lookUpReps, score } from "../scorecard";
import type { RepsResponse } from "../types";
import { localVoteBackend, type VoteBackend } from "../votes/backend";
import type { DeckCard } from "../vote-deck";
import { VoteBackendContext } from "../votes/hooks";
import { VotingSession } from "@/app/(app)/vote/voting-session";
import { deckOf } from "@/test/deck";
import { VoteSection } from "@/app/(app)/bills/[id]/vote-section";
import { ScorecardView } from "@/components/scorecard/local-scorecard";

function fakeEnv(): LocalEnv {
  const data = new Map<string, string>();
  return {
    storage: {
      getItem: (k) => data.get(k) ?? null,
      setItem: (k, v) => void data.set(k, v),
      removeItem: (k) => void data.delete(k),
    },
    events: new EventTarget(),
  };
}

// Server rendering uses getServerSnapshot, so this serves the client state there instead.
function asClient(backend: VoteBackend): VoteBackend {
  return { ...backend, getServerSnapshot: backend.getSnapshot };
}

function withBackend(backend: VoteBackend, child: ReactNode): string {
  return renderToStaticMarkup(createElement(VoteBackendContext.Provider, { value: backend }, child));
}

const cards: DeckCard[] = billList.items.slice(0, 3).map((bill) => ({ bill, summary: null }));

describe("VotingSession", () => {
  it("shows a placeholder until the browser's votes are known", () => {
    const store = createVoteStore({ env: fakeEnv, persist: () => undefined });
    const html = withBackend(localVoteBackend(store), createElement(VotingSession, deckOf(cards)));
    expect(html).toContain("Loading bills");
    expect(html).not.toContain(cards[0].bill.title);
  });

  it("doesn't offer bills already voted on, and says votes stay on this device", () => {
    const store = createVoteStore({ env: fakeEnv, persist: () => undefined });
    store.setVote(cards[0].bill.id, "yea");
    const html = withBackend(asClient(localVoteBackend(store)), createElement(VotingSession, deckOf(cards)));
    expect(html).toContain("Laws: 2 of 3 bills left");
    expect(html).toContain(cards[1].bill.title);
    expect(html).toContain("Your votes are kept on this device only.");
    expect(html).not.toContain("Sign in");
  });

  it("says when everything has been voted on", () => {
    const store = createVoteStore({ env: fakeEnv, persist: () => undefined });
    for (const c of cards) store.setVote(c.bill.id, "skip");
    const html = withBackend(asClient(localVoteBackend(store)), createElement(VotingSession, deckOf(cards)));
    expect(html).toContain("You&#x27;ve voted on every bill here");
  });

  it("warns when votes can only be kept in memory", () => {
    const store = createVoteStore({ env: () => ({ storage: null, events: null }), persist: () => undefined });
    const html = withBackend(asClient(localVoteBackend(store)), createElement(VotingSession, deckOf(cards)));
    expect(html).toContain("Votes won&#x27;t be saved on this browser.");
  });
});

describe("ScorecardView", () => {
  const reps = {
    state: "WY",
    district: 0,
    looked_up_at: "2026-10-04T00:00:00Z",
    members: [{ id: "H1", name: "Hana Hill", party: "", chamber: "House", state: "WY", district: 0 }],
  };
  const votes: LocalVotes = { "hr-119-1": { vote: "yea", at: "2026-10-04T00:00:00Z", title: "Act One" } };

  it("waits for the browser", () => {
    const html = renderToStaticMarkup(
      createElement(ScorecardView, { votes: {}, storage: "server", reps: null, scores: { status: "none" } })
    );
    expect(html).toContain("Loading your scorecard");
  });

  it("asks for an address first and says it isn't saved", () => {
    const html = renderToStaticMarkup(
      createElement(ScorecardView, { votes, storage: "device", reps: null, scores: { status: "none" } })
    );
    expect(html).toContain("Find your representatives");
    expect(html).toContain("isn&#x27;t saved in our database");
    expect(html).toContain("1 vote so far, kept on this device only.");
    expect(html).toContain('href="/my-votes"');
  });

  it("labels the seat and prompts for votes when there are none", () => {
    const html = renderToStaticMarkup(
      createElement(ScorecardView, { votes: {}, storage: "device", reps, scores: { status: "none" } })
    );
    expect(html).toContain("At-large");
    expect(html).toContain("Vote on a few bills to fill in your scorecard");
    expect(html).toContain('href="/vote">Start voting</a>');
  });

  it("says which district the address votes in when the lines changed (#375)", () => {
    vi.useFakeTimers({ now: new Date(2026, 9, 4, 12) });
    try {
      const election = {
        congress: 120,
        election_date: "2026-11-03",
        districts: [{ state: "TX", district: 10 }],
        current: [{ state: "TX", district: 37 }],
      };
      const tx = { ...reps, state: "TX", district: 37, election };
      const html = renderToStaticMarkup(
        createElement(ScorecardView, { votes: {}, storage: "device", reps: tx, scores: { status: "none" } })
      );
      expect(html).toContain("On November 3, 2026, this address votes in the election for <strong>TX-10</strong>");
      expect(html).toContain("current representative holds (TX-37)");
      expect(html).toContain('href="https://vote.gov" target="_blank" rel="noopener noreferrer"');

      const plain = renderToStaticMarkup(
        createElement(ScorecardView, { votes: {}, storage: "device", reps, scores: { status: "none" } })
      );
      expect(plain).not.toContain("vote.gov");
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows a card per member once scored, and an error with a retry", () => {
    const scores = score(votes, {
      H1: [{ bill_id: "hr-119-1", vote: "nay", vote_id: "v1", vote_date: "2025-01-01T00:00:00Z" }],
    });
    const html = renderToStaticMarkup(
      createElement(ScorecardView, { votes, storage: "device", reps, scores: { status: "ready", scores } })
    );
    expect(html).toContain("Hana Hill");
    expect(html).toContain("0%");
    expect(html).toContain("On 0 of the 1 bill you both voted yes or no on.");

    const failed = renderToStaticMarkup(
      createElement(ScorecardView, { votes, storage: "device", reps, scores: { status: "error" }, onRetry: () => {} })
    );
    expect(failed).toContain("couldn&#x27;t load");
    expect(failed).toContain("Try again");
  });
});

// Design 72, "Testing": vote on the bill page and on /vote, then build the scorecard, and every
// request is a GET apart from the one rep lookup, with no vote in any URL or body. The one
// exception is sharing (design 88): after the visitor clicks Share, the dialog fetches the card
// under /share/*, whose path holds only what the card prints. share-dialog.test.tsx checks that;
// here the share buttons render but send nothing.
describe("votes never leave the browser", () => {
  type Req = { method: string; url: string; body: string };
  let requests: Req[];

  const lookup: RepsResponse = {
    reps: [{ bioguide_id: "H1", first_name: "Hana", last_name: "Hill" }],
    senators: [
      { bioguide_id: "S1", first_name: "Sam", last_name: "Stone" },
      { bioguide_id: "S2", first_name: "Sue", last_name: "Sand" },
    ],
    districts: [{ state: "WY", district: 0 }],
  };

  beforeEach(() => {
    requests = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string | URL, init?: RequestInit) => {
        const url = String(input);
        requests.push({ method: init?.method ?? "GET", url, body: typeof init?.body === "string" ? init.body : "" });
        if (url.includes("/reps")) return Response.json(lookup);
        const m = /\/members\/(\w+)\/positions\?congress=(\d+)/.exec(url);
        if (m) {
          return Response.json({
            member_id: m[1],
            congress: Number(m[2]),
            rule: "final-passage-v1",
            positions: [
              { bill_id: "hr-119-1", vote: m[1] === "S2" ? "nay" : "yea", vote_id: "v1", vote_date: "2025-01-01T00:00:00Z" },
              { bill_id: "hr-119-2", vote: "nay", vote_id: "v2", vote_date: "2025-02-01T00:00:00Z" },
            ],
          });
        }
        return new Response("not found", { status: 404 });
      })
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it("holds for the bill page, /vote and /scorecard", async () => {
    const env = fakeEnv();
    const backend = localVoteBackend(createVoteStore({ env: () => env, persist: () => undefined }));

    // Bill page: the vote card, then a vote through the backend its buttons call. With the vote
    // in, the card offers to share it, and nothing is sent until that's clicked.
    withBackend(asClient(backend), createElement(VoteSection, { billId: "hr-119-1", billTitle: "Act One" }));
    await backend.setVote("hr-119-1", "yea", "Act One");
    expect(
      withBackend(asClient(backend), createElement(VoteSection, { billId: "hr-119-1", billTitle: "Act One" }))
    ).toContain("Share my vote");

    // /vote: the session, then a vote and a skip.
    withBackend(asClient(backend), createElement(VotingSession, deckOf(cards)));
    await backend.setVote("hr-119-2", "nay", "Act Two");
    await backend.setVote("hr-119-3", "skip", "Act Three");
    expect(requests).toEqual([]);

    // /scorecard: one lookup, then positions per member and congress.
    const reps = await lookUpReps("1 Main St, Cheyenne, WY");
    expect(reps).not.toBeNull();
    const repsStore = createRepsStore({ env: () => env });
    repsStore.saveReps(reps!);
    const votes = backend.getSnapshot().votes;
    const positions = await loadPositions(repsStore.getSnapshot().value!.members, congressesVotedIn(votes));
    const scores = score(votes, positions);
    const html = renderToStaticMarkup(
      createElement(ScorecardView, {
        votes,
        storage: "device",
        reps: repsStore.getSnapshot().value,
        scores: { status: "ready", scores },
      })
    );
    expect(html).toContain("Sue Sand");
    expect(requests.some((r) => r.url.includes("/share/"))).toBe(false);
    expect(scores.S2).toMatchObject({ compared: 2, matching: 1 });

    const repsRequests = requests.filter((r) => r.url.includes("/reps"));
    expect(repsRequests.map((r) => r.method)).toEqual(["POST"]);
    expect(repsRequests[0].url).not.toContain("Main");
    expect(requests.filter((r) => !r.url.includes("/reps")).every((r) => r.method === "GET")).toBe(true);
    const positionsUrls = requests.filter((r) => !r.url.includes("/reps")).map((r) => r.url.replace(/^.*\/api\/v1/, ""));
    expect(positionsUrls.sort()).toEqual([
      "/members/H1/positions?congress=119",
      "/members/S1/positions?congress=119",
      "/members/S2/positions?congress=119",
    ]);
    for (const r of requests) {
      for (const secret of ["hr-119-1", "hr-119-2", "hr-119-3", "yea", "nay", "skip"]) {
        expect(r.url).not.toContain(secret);
        expect(r.body).not.toContain(secret);
      }
    }

    // And the address stays out of the browser's storage.
    expect(JSON.stringify(repsStore.getSnapshot().value)).not.toContain("Main St");
  });
});
