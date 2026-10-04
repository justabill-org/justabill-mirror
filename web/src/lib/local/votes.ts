// Signed-out votes, kept in this browser's localStorage and never sent anywhere (design
// docs/design/72-account-free-voting.md, "Local store format"). Format of jab.votes.v1:
//   {"v":1,"votes":{"hr-119-1":{"vote":"yea","at":"2026-10-04T15:00:00.000Z","title":"…"}}}

import type { UserVoteChoice } from "@/lib/types";
import { createLocalStore, type LocalEnv, type LocalStore, type Parsed, type Snapshot } from "./storage";

export const VOTES_KEY = "jab.votes.v1";
export const VOTES_CORRUPT_KEY = "jab.votes.corrupt";
export const MAX_VOTES = 5000;
/** Titles are for scorecard rows; the cap keeps 5,000 votes well inside the ~5 MB quota. */
export const MAX_TITLE_LENGTH = 200;

export interface LocalVote {
  vote: UserVoteChoice;
  /** Time of the vote as `toISOString()` writes it (RFC 3339, UTC). On import, the newer one wins. */
  at: string;
  title?: string;
}

export type LocalVotes = Readonly<Record<string, LocalVote>>;

/** One exported vote. The fields match #55's `POST /me/votes:import` rows, plus the title. */
export interface VoteExportRow {
  bill_id: string;
  vote: UserVoteChoice;
  voted_at: string;
  title?: string;
}

export interface VotesExport {
  v: 1;
  exported_at: string;
  votes: VoteExportRow[];
}

export interface ImportResult {
  added: number;
  updated: number;
  unchanged: number;
}

export class VoteImportError extends Error {}

const EMPTY: LocalVotes = Object.freeze({});
const CHOICES: ReadonlySet<string> = new Set<UserVoteChoice>(["yea", "nay", "skip"]);
const BILL_ID = /^[a-z]+-\d{1,3}-\d{1,6}$/;
const MAX_IMPORT_BYTES = 2_000_000;

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === "object" && x !== null && !Array.isArray(x);
}

/**
 * A time as `toISOString()` writes it, or null if it can't be read. Anything `Date.parse` reads
 * ("2026-10-04", "Oct 4 2026") is rewritten, since the API decodes `voted_at` as RFC 3339 and
 * rejects a whole import batch over one other form (#647). Years outside 0000–9999 have no
 * RFC 3339 form, so they count as unreadable.
 */
function canonicalTime(x: unknown): string | null {
  if (typeof x !== "string") return null;
  const ms = Date.parse(x);
  if (Number.isNaN(ms)) return null;
  const iso = new Date(ms).toISOString();
  return /^\d{4}-/.test(iso) ? iso : null;
}

function cleanTitle(x: unknown): string | undefined {
  if (typeof x !== "string") return undefined;
  const t = x.trim();
  if (!t) return undefined;
  return t.length > MAX_TITLE_LENGTH ? `${t.slice(0, MAX_TITLE_LENGTH - 1)}…` : t;
}

function entry(vote: unknown, time: unknown, title: unknown): LocalVote | null {
  const at = canonicalTime(time);
  if (typeof vote !== "string" || !CHOICES.has(vote) || at === null) return null;
  const t = cleanTitle(title);
  return t ? { vote: vote as UserVoteChoice, at, title: t } : { vote: vote as UserVoteChoice, at };
}

/** Reads jab.votes.v1. Unknown versions are rejected; bad entries are dropped and flagged lossy. */
export function parseVotes(data: unknown): Parsed<LocalVotes> {
  if (!isRecord(data) || data.v !== 1 || !isRecord(data.votes)) return null;
  const votes: Record<string, LocalVote> = {};
  let lossy = false;
  for (const [billId, raw] of Object.entries(data.votes)) {
    const e = isRecord(raw) && BILL_ID.test(billId) ? entry(raw.vote, raw.at, raw.title) : null;
    if (e) votes[billId] = e;
    else lossy = true;
  }
  return { value: capVotes(votes), lossy };
}

/** Keeps at most MAX_VOTES: the oldest skips go first, then the oldest votes. */
export function capVotes(votes: Record<string, LocalVote>): Record<string, LocalVote> {
  const ids = Object.keys(votes);
  if (ids.length <= MAX_VOTES) return votes;
  const rank = (id: string) => (votes[id].vote === "skip" ? 0 : 1);
  ids.sort((a, b) => rank(a) - rank(b) || Date.parse(votes[a].at) - Date.parse(votes[b].at));
  const kept = { ...votes };
  for (const id of ids.slice(0, ids.length - MAX_VOTES)) delete kept[id];
  return kept;
}

function parseExport(text: string): VoteExportRow[] {
  if (text.length > MAX_IMPORT_BYTES) throw new VoteImportError("That file is too large to be a votes file.");
  let data: unknown;
  try {
    data = JSON.parse(text);
  } catch {
    throw new VoteImportError("That file isn't valid JSON.");
  }
  if (!isRecord(data) || data.v !== 1 || !Array.isArray(data.votes)) {
    throw new VoteImportError("That file isn't a Just a Bill votes export.");
  }
  return data.votes.map((row, i) => {
    const e = isRecord(row) ? entry(row.vote, row.voted_at, row.title) : null;
    if (!e || !isRecord(row) || typeof row.bill_id !== "string" || !BILL_ID.test(row.bill_id)) {
      throw new VoteImportError(`Vote ${i + 1} in that file isn't valid, so nothing was imported.`);
    }
    return { bill_id: row.bill_id, vote: e.vote, voted_at: e.at, title: e.title };
  });
}

export interface VoteStoreOptions {
  env?: () => LocalEnv | null;
  now?: () => Date;
  /** Asks the browser not to evict storage (Safari's 7-day rule). Defaults to navigator.storage. */
  persist?: () => Promise<boolean> | undefined;
}

export interface LocalVoteStore {
  subscribe(listener: () => void): () => void;
  getSnapshot(): Snapshot<LocalVotes>;
  getServerSnapshot(): Snapshot<LocalVotes>;
  setVote(billId: string, vote: UserVoteChoice, title?: string): Snapshot<LocalVotes>;
  clearVote(billId: string): void;
  clearAll(): void;
  /** Clears every vote except those on the given bills. */
  keepOnly(billIds: readonly string[]): void;
  exportVotes(): VotesExport;
  /** Merges an exported file; the newer vote wins. Throws VoteImportError with a message to show. */
  importVotes(text: string): ImportResult;
  /** Data moved aside because it couldn't be read, for a "download it" offer. */
  readCorrupt(): string | null;
}

function defaultPersist(): Promise<boolean> | undefined {
  if (typeof navigator === "undefined") return undefined;
  return navigator.storage?.persist?.();
}

export function createVoteStore(opts: VoteStoreOptions = {}): LocalVoteStore {
  const now = opts.now ?? (() => new Date());
  const persist = opts.persist ?? defaultPersist;
  let persistRequested = false;
  const store: LocalStore<LocalVotes> = createLocalStore<LocalVotes>({
    key: VOTES_KEY,
    corruptKey: VOTES_CORRUPT_KEY,
    empty: EMPTY,
    parse: parseVotes,
    serialize: (votes) => ({ v: 1, votes }),
    env: opts.env,
  });

  function requestPersistence() {
    if (persistRequested) return;
    persistRequested = true;
    try {
      persist()?.catch(() => undefined);
    } catch {
      // Best effort: browsers without the Storage API just keep the default policy.
    }
  }

  return {
    subscribe: store.subscribe,
    getSnapshot: store.getSnapshot,
    getServerSnapshot: store.getServerSnapshot,
    setVote(billId, vote, title) {
      const next = store.update((votes) => {
        const e = entry(vote, now().toISOString(), title ?? votes[billId]?.title);
        return e ? capVotes({ ...votes, [billId]: e }) : votes;
      });
      if (next.persistence === "device") requestPersistence();
      return next;
    },
    clearVote(billId) {
      store.update((votes) => {
        if (!(billId in votes)) return votes;
        const rest = { ...votes };
        delete rest[billId];
        return rest;
      });
    },
    clearAll() {
      store.update(() => EMPTY);
    },
    keepOnly(billIds) {
      store.update((votes) => {
        const kept: Record<string, LocalVote> = {};
        for (const id of billIds) if (id in votes) kept[id] = votes[id];
        return kept;
      });
    },
    exportVotes() {
      const votes = Object.entries(store.getSnapshot().value)
        .sort(([, a], [, b]) => Date.parse(b.at) - Date.parse(a.at))
        .map(([billId, v]) => ({ bill_id: billId, vote: v.vote, voted_at: v.at, title: v.title }));
      return { v: 1, exported_at: now().toISOString(), votes };
    },
    importVotes(text) {
      const rows = parseExport(text);
      const result: ImportResult = { added: 0, updated: 0, unchanged: 0 };
      store.update((votes) => {
        const merged: Record<string, LocalVote> = { ...votes };
        for (const row of rows) {
          const have = merged[row.bill_id];
          const title = row.title ?? have?.title;
          if (!have) {
            result.added++;
          } else if (Date.parse(row.voted_at) > Date.parse(have.at)) {
            result.updated++;
          } else {
            result.unchanged++;
            continue;
          }
          merged[row.bill_id] = title ? { vote: row.vote, at: row.voted_at, title } : { vote: row.vote, at: row.voted_at };
        }
        return capVotes(merged);
      });
      return result;
    },
    readCorrupt: store.readCorrupt,
  };
}
