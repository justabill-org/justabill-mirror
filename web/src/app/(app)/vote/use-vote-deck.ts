import { useEffect, useState, useSyncExternalStore } from "react";
import { listBills, listBillsWithCards } from "@/lib/api";
import type { BillFilters } from "@/lib/bill-filters";
import { reportError } from "@/lib/obs/browser";
import { maxOffsetFor } from "@/lib/paging";
import type { PaginatedResult } from "@/lib/types";
import {
  createDeckReader,
  deckListParams,
  type DeckItem,
  type DeckReader,
  type DeckSnapshot,
  type DeckSource,
} from "@/lib/vote-deck";

export interface VoteDeckOptions {
  /** The congress in session, which the filters' default congress stands for. */
  current: number | undefined;
  /** The first batch, already read: the default deck's, rendered into the page. */
  first?: PaginatedResult<DeckItem>;
  /**
   * The signed-in user's ID token: their batches leave out what they voted on (`unvoted=true`).
   * Absent signed out, when nothing about the user's votes is sent.
   */
  getIdToken?: () => Promise<string>;
  /** The user's votes (and skips), by bill ID: those bills aren't dealt. */
  votes: Readonly<Record<string, unknown>>;
}

export type VoteDeck = DeckSnapshot & {
  /** Deals the next card; call it once the current one is voted on. */
  next(): void;
  /** Reads the batch that failed again. */
  retry(): void;
};

/**
 * The /vote deck for `filters` (#797): the /bills list under them, one card at a time, from
 * `GET /bills?include=summary,card` read in the browser a batch at a time. The filters and the
 * user are fixed for the deck's life: mount a new one (a new `key`) when either changes.
 */
export function useVoteDeck(filters: BillFilters, { current, first, getIdToken, votes }: VoteDeckOptions): VoteDeck {
  const [{ reader, setVotes }] = useState(() => {
    // The latest votes, which the reader checks each bill against: set again on every change.
    let known = votes;
    const params = deckListParams(filters, current);
    const source: DeckSource = {
      unvoted: getIdToken !== undefined,
      maxOffset: maxOffsetFor(params.q),
      isVoted: (id) => Object.hasOwn(known, id),
      onError: (err) => reportError(err, "/vote"),
      list: getIdToken
        ? async (offset, limit) => listBillsWithCards({ ...params, unvoted: true, offset, limit }, await getIdToken())
        : (offset, limit) => listBillsWithCards({ ...params, offset, limit }),
    };
    if (getIdToken) source.count = async () => (await listBills({ ...params, limit: 1 })).total;
    const deck: DeckReader = createDeckReader(source, { first });
    return { reader: deck, setVotes: (next: VoteDeckOptions["votes"]) => void (known = next) };
  });
  useEffect(() => setVotes(votes), [setVotes, votes]);

  useEffect(() => {
    reader.start();
    return () => reader.stop();
  }, [reader]);

  const snapshot = useSyncExternalStore(reader.subscribe, reader.getSnapshot, reader.getSnapshot);
  return { ...snapshot, next: reader.next, retry: reader.retry };
}
