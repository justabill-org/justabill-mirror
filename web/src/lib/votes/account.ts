// Signed-in votes, kept in the account (#138, docs/design/55-social-auth.md): a vote goes to
// POST /bills/{id}/vote, DELETE /bills/{id}/vote removes it (#593), and the list comes from GET /me/votes, which names each vote's bill (#495).
// Where it doesn't (an older API, or a bill that's gone), titles come from this session's votes and
// from the votes kept on this device.

import type { PaginatedResult, UserVote, UserVoteChoice } from "@/lib/types";
import type { LocalVote, LocalVotes } from "@/lib/local/votes";
import type { VoteBackend, VoteState } from "./backend";

/** GET /me/votes pages: the API's largest page, and the deepest offset it accepts. */
export const VOTES_PAGE_SIZE = 100;
export const MAX_VOTES_OFFSET = 10_000;

export interface AccountVotesApi {
  getMyVotes(token: string, params: { offset?: number; limit?: number }): Promise<PaginatedResult<UserVote>>;
  castVote(token: string, billId: string, vote: UserVoteChoice): Promise<unknown>;
  deleteVote(token: string, billId: string): Promise<unknown>;
}

export interface AccountVoteOptions {
  /** A current ID token (useUser().getIdToken). */
  getIdToken: () => Promise<string>;
  api: AccountVotesApi;
  /** Titles for bills, e.g. from this device's local votes; read when the votes load. */
  titles?: () => LocalVotes;
  now?: () => Date;
}

const LOADING: VoteState = { votes: Object.freeze({}), storage: "server" };

/** Every vote in the account, newest first, a page at a time. */
export async function loadAccountVotes(api: AccountVotesApi, token: string): Promise<UserVote[]> {
  const all: UserVote[] = [];
  for (let offset = 0; offset <= MAX_VOTES_OFFSET; offset += VOTES_PAGE_SIZE) {
    const page = await api.getMyVotes(token, { offset, limit: VOTES_PAGE_SIZE });
    all.push(...page.items);
    if (page.items.length < VOTES_PAGE_SIZE || all.length >= page.total) break;
  }
  return all;
}

/**
 * The account's votes as a vote backend. It loads them on the first subscription, so a page that
 * shows no votes doesn't fetch them. A vote cast or removed while they load wins over the loaded one.
 */
export function createAccountVoteBackend(opts: AccountVoteOptions): VoteBackend {
  const now = opts.now ?? (() => new Date());
  const listeners = new Set<() => void>();
  let state: VoteState = LOADING;
  let loading = false;
  // A refresh asked for while a load runs: that load may predate the change, so load once more.
  let again = false;
  let loaded = false;
  // Votes cast (or removed: null) in this session: they win over a load that started before them,
  // and cast ones keep their titles.
  let cast: Record<string, LocalVote | null> = {};

  function set(next: VoteState) {
    state = next;
    listeners.forEach((l) => l());
  }

  function withTitle(billId: string, v: LocalVote, titles: LocalVotes): LocalVote {
    const title = v.title ?? cast[billId]?.title ?? titles[billId]?.title;
    return title ? { ...v, title } : v;
  }

  async function load() {
    if (loading) {
      again = true;
      return;
    }
    loading = true;
    if (state.storage === "unavailable") set(LOADING);
    try {
      const rows = await loadAccountVotes(opts.api, await opts.getIdToken());
      const titles = opts.titles?.() ?? {};
      const votes: Record<string, LocalVote> = {};
      for (const r of rows) {
        const v: LocalVote = { vote: r.vote, at: r.voted_at };
        if (r.title) v.title = r.title;
        votes[r.bill_id] = withTitle(r.bill_id, v, titles);
      }
      for (const [billId, v] of Object.entries(cast)) {
        if (v) votes[billId] = v;
        else delete votes[billId];
      }
      loaded = true;
      set({ votes, storage: "account" });
    } catch (err) {
      console.error("Failed to load your votes:", err);
      set({ votes: Object.freeze({}), storage: "unavailable" });
    } finally {
      loading = false;
    }
    if (again) {
      again = false;
      await load();
    }
  }

  return {
    subscribe(listener) {
      listeners.add(listener);
      if (!loaded && !loading) void load();
      return () => listeners.delete(listener);
    },
    getSnapshot: () => state,
    getServerSnapshot: () => LOADING,
    async setVote(billId, vote, title) {
      if (state.storage === "unavailable") throw new Error("votes are unavailable");
      await opts.api.castVote(await opts.getIdToken(), billId, vote);
      const t = title ?? state.votes[billId]?.title;
      const entry: LocalVote = t ? { vote, at: now().toISOString(), title: t } : { vote, at: now().toISOString() };
      cast = { ...cast, [billId]: entry };
      if (state.storage === "account") set({ ...state, votes: { ...state.votes, [billId]: entry } });
    },
    async clearVote(billId) {
      if (state.storage === "unavailable") throw new Error("votes are unavailable");
      await opts.api.deleteVote(await opts.getIdToken(), billId);
      cast = { ...cast, [billId]: null };
      if (state.storage === "account" && billId in state.votes) {
        const votes = { ...state.votes };
        delete votes[billId];
        set({ ...state, votes });
      }
    },
    refresh() {
      void load();
    },
  };
}
