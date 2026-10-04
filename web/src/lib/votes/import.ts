// Moving the votes kept on this device into the account, with the user's consent (#138, design
// docs/design/55-social-auth.md "Migration"). Nothing is sent until they choose to add them.

import { MAX_IMPORT_VOTES, type ImportVotesResult } from "@/lib/api";
import type { LocalVotes } from "@/lib/local/votes";
import type { UserVoteChoice } from "@/lib/types";

export interface ImportRow {
  bill_id: string;
  vote: UserVoteChoice;
  voted_at: string;
}

export type ImportCall = (token: string, rows: ImportRow[]) => Promise<ImportVotesResult>;

export interface ImportTotals {
  /** Votes added to the account. */
  imported: number;
  /** Votes the account already had (it wins), or on bills the API doesn't know. */
  skipped: number;
  /**
   * Bills whose votes the daily vote cap held back, and those in batches not sent after it did:
   * they weren't added, so they stay on the device for a later import (#458).
   */
  kept: string[];
}

/** The local votes as import rows, oldest first, in batches the API accepts. */
export function importBatches(votes: LocalVotes, size: number = MAX_IMPORT_VOTES): ImportRow[][] {
  const rows = Object.entries(votes)
    .map(([billId, v]) => ({ bill_id: billId, vote: v.vote, voted_at: v.at }))
    .sort((a, b) => Date.parse(a.voted_at) - Date.parse(b.voted_at));
  const batches: ImportRow[][] = [];
  for (let i = 0; i < rows.length; i += size) batches.push(rows.slice(i, i + size));
  return batches;
}

/**
 * Sends every local vote to POST /me/votes:import, a batch at a time, oldest first. A failed batch
 * rejects, and the batches before it stay imported: running it again is safe, since the account's
 * votes win. Once the daily vote cap holds votes back, it sends no more batches, and lists the held
 * and unsent votes in `kept`.
 */
export async function importLocalVotes(
  votes: LocalVotes,
  getIdToken: () => Promise<string>,
  call: ImportCall
): Promise<ImportTotals> {
  const totals: ImportTotals = { imported: 0, skipped: 0, kept: [] };
  const batches = importBatches(votes);
  for (const [i, batch] of batches.entries()) {
    const r = await call(await getIdToken(), batch);
    totals.imported += r.imported;
    totals.skipped += r.skipped;
    if (r.capped_bill_ids?.length) {
      totals.kept = [...r.capped_bill_ids, ...batches.slice(i + 1).flatMap((b) => b.map((row) => row.bill_id))];
      break;
    }
  }
  return totals;
}
