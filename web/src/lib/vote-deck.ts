import { type BillFilters, billListParams } from "./bill-filters";
import { parseBillView } from "./bill-views";
import type { Bill, BillCardFacts, BillListItem, BillListParams, BillSummary, PaginatedResult } from "./types";

// The /vote deck (#797): the /bills list under the same filters, one card at a time, read in
// batches until the list ends. A plain module, so the server page and the browser share it.

/** One card in the /vote deck. */
export interface DeckCard {
  bill: Bill;
  summary: BillSummary | null;
  /** How it passed, when it became law and its CRS lead (#662); null or absent without them. */
  card?: BillCardFacts | null;
}

/** A bill from `GET /bills?include=summary,card`, as the deck reads it. */
export type DeckItem = BillListItem & { card?: BillCardFacts | null };

/** How many bills the deck reads at a time. */
export const DECK_BATCH = 20;

/** The deck reads the next batch once this many cards (or fewer) are left to deal. */
export const DECK_PREFETCH = 5;

/** Splits `GET /bills?include=summary,card` items into the bill, its summary and its card facts. */
export function toDeckCards(items: DeckItem[]): DeckCard[] {
  return items.map(({ summary, card, ...bill }) => ({ bill, summary: summary ?? null, card: card ?? null }));
}

/**
 * The list the deck reads for the filters: the /bills view's statuses (any of them, one list) and
 * the other filters, without the page. `current` is the congress in session, when it's known.
 */
export function deckListParams(f: BillFilters, current: number | undefined): BillListParams {
  const { statuses } = parseBillView(f.view);
  const params: BillListParams = billListParams(f, current);
  if (statuses.length > 0) params.status = statuses;
  return params;
}

/**
 * The localStorage keys of the filter /vote used to remember (#663). The filter lives in the URL
 * now (#797), so the deck removes them on its first visit and nothing reads them.
 */
export const OLD_VOTE_FILTER_KEYS = ["jab.vote-filter.v1", "jab.vote-filter.corrupt"] as const;

/**
 * Removes the old /vote filter from the storage `storage()` returns; storage that can't be reached
 * is left alone. It takes a getter because reading `window.localStorage` itself throws where the
 * browser blocks site data.
 */
export function forgetOldVoteFilter(storage: () => Pick<Storage, "removeItem"> | null | undefined): void {
  try {
    const s = storage();
    for (const key of OLD_VOTE_FILTER_KEYS) s?.removeItem(key);
  } catch {
    // Blocked storage holds nothing to remove.
  }
}

/**
 * Where the deck reads its bills: the list under the filters, a batch at a time.
 */
export interface DeckSource {
  /** One batch of the list from `offset`. Signed in, only the bills the user hasn't voted on. */
  list(offset: number, limit: number): Promise<PaginatedResult<DeckItem>>;
  /**
   * The list's total for everyone, when the batches leave the user's votes out (signed in): they
   * can't tell how many bills the filters hold. Absent signed out, where the batches' total is it.
   */
  count?: () => Promise<number>;
  /**
   * Whether the batches leave out the bills the user has voted on (`unvoted=true`). Each vote then
   * takes a bill out of the list, so the next batch starts that much earlier.
   */
  unvoted: boolean;
  /** Whether the user has voted on (or skipped) the bill: such bills aren't dealt. */
  isVoted(billId: string): boolean;
  /** The deepest offset the API pages to for this list (`maxOffsetFor`). */
  maxOffset: number;
  /** Told about a batch that couldn't be read. */
  onError?: (err: unknown) => void;
}

/**
 * - loading: no card to deal yet, and a batch is being read.
 * - ready: a card to vote on.
 * - failed: no card left, and the last batch couldn't be read: retry() reads it again.
 * - end: every bill under the filters has been voted on or skipped.
 * - empty: the filters match no bill.
 * - capped: no card left, and the list goes on past the deepest page the API serves.
 */
export type DeckStatus = "loading" | "ready" | "failed" | "end" | "empty" | "capped";

export interface DeckSnapshot {
  status: DeckStatus;
  /** The card to vote on (ready), else null. */
  card: DeckCard | null;
  /** How many bills the filters hold; undefined until the first read. */
  total: number | undefined;
  /**
   * How many of them are left to vote on, as far as the batches read so far tell (bills further
   * down that this device voted on count as left until a batch reaches them).
   */
  left: number | undefined;
}

export interface DeckReader {
  subscribe(listener: () => void): () => void;
  getSnapshot(): DeckSnapshot;
  /** Starts (or resumes) reading. */
  start(): void;
  /** Stops reading: a batch on its way is dropped. */
  stop(): void;
  /** Deals the next card, once the current one is voted on, and reads on when few are left. */
  next(): void;
  /** Reads the batch that failed again. */
  retry(): void;
}

/** Options for createDeckReader; the defaults are DECK_BATCH and DECK_PREFETCH. */
export interface DeckReaderOptions {
  /** The first batch, already read (the default deck, rendered into the page). */
  first?: PaginatedResult<DeckItem>;
  batch?: number;
  prefetch?: number;
}

/**
 * The deck over `source`: it deals the list's bills in order, skipping the ones voted on and any
 * bill an earlier batch already held (a sync can reorder the list between batches), and reads the
 * next batch, one at a time, once `prefetch` cards or fewer are left. Batches that hold nothing to
 * deal are read on until one does or the list ends. A bill that moves up into the part of the list
 * already read is missed until the next visit; one that moves down is dropped as already seen.
 */
export function createDeckReader(
  source: DeckSource,
  { first, batch = DECK_BATCH, prefetch = DECK_PREFETCH }: DeckReaderOptions = {}
): DeckReader {
  const listeners = new Set<() => void>();
  const queue: DeckCard[] = [];
  // Every bill a batch held: what's been dealt or passed over, so none is dealt twice.
  const seen = new Set<string>();
  // How many places of the list the batches covered, and the first batch's total.
  let covered = 0;
  let base: number | undefined;
  let total: number | undefined;
  let done = false;
  let capped = false;
  let reading = false;
  let failed = false;
  let running = false;
  let snapshot: DeckSnapshot;

  const votedSeen = () => {
    let n = 0;
    for (const id of seen) if (source.isVoted(id)) n++;
    return n;
  };

  function build(): DeckSnapshot {
    const left = base === undefined ? undefined : Math.max(0, base - votedSeen());
    const card = queue[0] ?? null;
    let status: DeckStatus = "loading";
    if (card) status = "ready";
    else if (failed) status = "failed";
    else if (done) status = total === 0 ? "empty" : "end";
    else if (capped) status = "capped";
    return { status, card, total, left };
  }

  function notify() {
    snapshot = build();
    for (const l of listeners) l();
  }

  function take(page: PaginatedResult<DeckItem>, offset: number) {
    base ??= page.total;
    if (!source.count) total ??= page.total;
    covered += page.items.length;
    for (const card of toDeckCards(page.items)) {
      if (seen.has(card.bill.id)) continue;
      seen.add(card.bill.id);
      if (!source.isVoted(card.bill.id)) queue.push(card);
    }
    if (page.items.length < batch || offset + page.items.length >= page.total) done = true;
  }

  // Where the next batch starts. In a list that leaves out the user's votes, each bill voted on
  // since it was read has left the list; counting one too many only repeats a bill, which `seen` drops.
  const nextOffset = () => (source.unvoted ? Math.max(0, covered - votedSeen()) : covered);

  async function pump() {
    while (running && !reading && !failed && !done && !capped && queue.length <= prefetch) {
      const offset = nextOffset();
      if (offset > source.maxOffset) {
        capped = true;
        notify();
        return;
      }
      reading = true;
      notify();
      try {
        if (source.count && total === undefined) total = await source.count();
        const page = await source.list(offset, batch);
        if (running) take(page, offset);
      } catch (err) {
        if (running) {
          failed = true;
          source.onError?.(err);
        }
      }
      reading = false;
      notify();
    }
  }

  if (first) {
    if (source.unvoted) total = first.total;
    else take(first, 0);
  }
  snapshot = build();

  return {
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    getSnapshot: () => snapshot,
    start() {
      running = true;
      void pump();
    },
    stop() {
      running = false;
    },
    next() {
      queue.shift();
      notify();
      void pump();
    },
    retry() {
      failed = false;
      notify();
      void pump();
    },
  };
}
