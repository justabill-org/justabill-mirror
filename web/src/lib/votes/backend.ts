// The seam between the UI and wherever votes are kept. Signed out, that's this browser
// (lib/local/votes.ts); signed in, the account (lib/votes/account.ts: POST and DELETE
// /bills/{id}/vote, GET /me/votes), provided through VoteBackendContext by AccountVotesProvider (#138).

import { ApiError } from "@/lib/api";
import type { UserVoteChoice } from "@/lib/types";
import { createVoteStore, type LocalVoteStore, type LocalVotes } from "@/lib/local/votes";

/**
 * `device`: kept in this browser. `memory`: this page only, so the UI warns that votes won't be
 * saved. `account`: the signed-in account (#138). `server`: nothing known yet (server render, or
 * the session or the account's votes are still loading). `unavailable`: signed in, but the
 * account's votes couldn't be loaded, so nothing can be shown or saved until a retry works.
 */
export type VoteStorage = "device" | "memory" | "account" | "server" | "unavailable";

/** What a failed save says, per choice, so the visitor knows which vote Try again resends. */
export const VOTE_FAILED: Record<UserVoteChoice, string> = {
  yea: "Your Yea vote couldn't be saved.",
  nay: "Your Nay vote couldn't be saved.",
  skip: "Your Skip couldn't be saved.",
};

/**
 * What a vote past the account's daily cap says (#646). The API counts votes (skips too) over the
 * last 24 hours, so retrying right away fails the same way: the UI offers no Try again.
 */
export const VOTE_CAP_REACHED =
  "You've reached the daily vote limit, so this vote wasn't saved. You can vote again within 24 hours.";

/** Whether a failed save was refused for the daily vote cap (429 `daily_vote_cap`). */
export function isVoteCapReached(err: unknown): boolean {
  return err instanceof ApiError && err.status === 429 && err.code === "daily_vote_cap";
}

export interface VoteState {
  readonly votes: LocalVotes;
  readonly storage: VoteStorage;
}

export interface VoteBackend {
  subscribe(listener: () => void): () => void;
  getSnapshot(): VoteState;
  getServerSnapshot(): VoteState;
  setVote(billId: string, vote: UserVoteChoice, title?: string): Promise<void>;
  /** Removes the vote on a bill (#593); a bill with no vote is left as it is. */
  clearVote(billId: string): Promise<void>;
  /** Loads the votes again, e.g. after an import or a failed load. Local stores don't need it. */
  refresh?(): void;
}

/**
 * A backend with a fixed state whose writes fail: `server` while the session is still being
 * worked out (a vote then can't land in the wrong place), `unavailable` when the account failed.
 */
export function fixedVoteBackend(storage: "server" | "unavailable", refresh?: () => void): VoteBackend {
  const state: VoteState = { votes: Object.freeze({}), storage };
  const refuse = () => Promise.reject(new Error(`votes are ${storage === "server" ? "loading" : "unavailable"}`));
  return {
    subscribe: () => () => {},
    getSnapshot: () => state,
    getServerSnapshot: () => state,
    setVote: refuse,
    clearVote: refuse,
    ...(refresh ? { refresh } : {}),
  };
}

let localStore: LocalVoteStore | null = null;

/** The page-wide local vote store (created lazily, so importing this never touches window). */
export function localVotes(): LocalVoteStore {
  localStore ??= createVoteStore();
  return localStore;
}

/** Adapts a local store to the backend interface, keeping snapshots referentially stable. */
export function localVoteBackend(store: LocalVoteStore = localVotes()): VoteBackend {
  const cache = new WeakMap<object, VoteState>();
  const toState = (s: ReturnType<LocalVoteStore["getSnapshot"]>): VoteState => {
    let state = cache.get(s);
    if (!state) {
      state = { votes: s.value, storage: s.persistence };
      cache.set(s, state);
    }
    return state;
  };
  return {
    subscribe: store.subscribe,
    getSnapshot: () => toState(store.getSnapshot()),
    getServerSnapshot: () => toState(store.getServerSnapshot()),
    async setVote(billId, vote, title) {
      store.setVote(billId, vote, title);
    },
    async clearVote(billId) {
      store.clearVote(billId);
    },
  };
}
