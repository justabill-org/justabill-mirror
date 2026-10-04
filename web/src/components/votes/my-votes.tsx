"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { Button, buttonClasses } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Pagination } from "@/components/ui/pagination";
import { Skeleton } from "@/components/ui/skeleton";
import { loadBillStatuses } from "@/lib/bill-statuses";
import type { LocalVote, LocalVoteStore } from "@/lib/local/votes";
import {
  activeFilterCount,
  MY_VOTES_ALL,
  MY_VOTES_FILTER_BAR_MIN,
  MY_VOTES_PAGE_SIZE,
  myVoteFacets,
  myVoteIds,
  myVoteRows,
  myVoteSummary,
  NO_BILL_STATUSES,
  votedBillStatus,
  type BillStatuses,
  type MyVotesQuery,
} from "@/lib/my-votes";
import type { VoteStorage } from "@/lib/votes/backend";
import { useVoteBackend, useVotes } from "@/lib/votes/hooks";
import { CompactVoteRow } from "./compact-vote-row";
import { DeviceTools } from "./device-tools";
import { VoteFilterBar } from "./vote-filter-bar";
import { VoteSummary } from "./vote-summary";

/** The view's bills, as ordered when its filters or search last changed (null: not taken yet). */
interface View {
  key: string | null;
  ids: readonly string[];
}

export interface MyVotesProps {
  /** The browser's vote store for the device tools; defaults to the page-wide one. For tests. */
  deviceStore?: LocalVoteStore;
  /** Where the bills stand; defaults to every congress's `GET /bill-statuses`. For tests. */
  loadStatuses?: () => Promise<BillStatuses>;
}

/**
 * Every bill the visitor voted on (#738), redesigned for hundreds of votes (#843): a summary, one
 * filter bar, and compact rows whose vote buttons open behind Change. Votes come from useVotes()
 * and saves go through the vote backend; a row keeps its place when its vote changes or is
 * removed. Where each bill stands comes from the public lists, joined to the votes here.
 */
export function MyVotes({ deviceStore, loadStatuses = loadBillStatuses }: MyVotesProps) {
  const { votes, storage } = useVotes();
  // Null until the lists load; the status filter and the Became law figure wait for them.
  const [loadedStatuses, setStatuses] = useState<BillStatuses | null>(null);
  const [query, setQuery] = useState<MyVotesQuery>(MY_VOTES_ALL);
  const [page, setPage] = useState(0);
  // Votes this page removed, by bill, so their rows can offer Undo until the view changes.
  const [removed, setRemoved] = useState<Record<string, LocalVote>>({});
  const [view, setView] = useState<View>({ key: null, ids: [] });
  // Bumped when an import or a clear replaces the votes wholesale, so the order is taken again.
  const [revision, setRevision] = useState(0);
  const listTop = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let live = true;
    void loadStatuses().then((s) => {
      if (live) setStatuses(s);
    });
    return () => {
      live = false;
    };
  }, [loadStatuses]);

  const statuses = loadedStatuses ?? NO_BILL_STATUSES;
  const statusesLoading = loadedStatuses === null;

  const loaded = storage !== "server" && storage !== "unavailable";
  // The order is taken once the votes load and again whenever a filter or the search changes,
  // never when a vote changes: a changed row stays where it is, even under a filter it left.
  const viewKey = loaded ? `${JSON.stringify(query)}\n${revision}` : null;
  let ids = view.ids;
  if (view.key !== viewKey) {
    ids = viewKey === null ? [] : myVoteIds(votes, statuses, query);
    setView({ key: viewKey, ids });
    setRemoved({});
    setPage(0);
  }

  if (storage === "server") {
    return <Skeleton className="h-64 w-full rounded-xl" role="status" aria-label="Loading your votes" />;
  }
  if (storage === "unavailable") return <VotesUnavailable />;

  const tools =
    storage === "account" ? null : <DeviceTools store={deviceStore} onChange={() => setRevision((r) => r + 1)} />;
  // A removed vote's row stays to offer Undo, so the page is empty only with neither.
  const empty = Object.keys(votes).length === 0 && Object.keys(removed).length === 0;
  const summary = myVoteSummary(votes, statuses);
  const rows = myVoteRows(ids, votes, statuses, query, removed);
  const filtered = activeFilterCount(query) > 0 || query.search.trim() !== "";
  const pages = Math.max(1, Math.ceil(rows.length / MY_VOTES_PAGE_SIZE));
  const current = Math.min(page, pages - 1);

  const row = (id: string) => (
    <CompactVoteRow
      key={id}
      billId={id}
      vote={votes[id]}
      removedVote={removed[id]}
      status={statusesLoading ? "loading" : votedBillStatus(statuses, id)}
      onRemoved={(billId, vote) => setRemoved((r) => ({ ...r, [billId]: vote }))}
      onRestored={(billId) =>
        setRemoved((r) => {
          const rest = { ...r };
          delete rest[billId];
          return rest;
        })
      }
    />
  );

  // The device tools keep their place whether or not there are votes, so the message of an import
  // that fills an empty page (or a clear that empties it) survives the switch.
  return (
    <div className="space-y-8">
      {empty ? (
        <NoVotes storage={storage} />
      ) : (
        <div className="space-y-8">
          <VoteSummary
            summary={summary}
            query={query}
            loading={statusesLoading}
            note={<StorageNote storage={storage} />}
            onQuery={setQuery}
          />

          <div ref={listTop} className="scroll-mt-20 space-y-3">
            {summary.total >= MY_VOTES_FILTER_BAR_MIN && (
              <VoteFilterBar
                query={query}
                facets={myVoteFacets(votes, statuses, query)}
                statusesLoading={statusesLoading}
                onQuery={setQuery}
              />
            )}
            <div className="flex min-h-8 flex-wrap items-center justify-between gap-x-4">
              <p role="status" className="text-sm text-muted-foreground">
                {filtered
                  ? `${rows.length.toLocaleString()} of ${summary.total.toLocaleString()} votes`
                  : "Newest first"}
              </p>
              {filtered && (
                <Button variant="ghost" size="sm" onClick={() => setQuery(MY_VOTES_ALL)}>
                  Clear filters
                </Button>
              )}
            </div>

            {rows.length === 0 ? (
              <p className="border-t border-border py-10 text-center text-muted-foreground">
                None of your votes match these filters.
              </p>
            ) : (
              <>
                <ul className="divide-y divide-border border-y border-border">
                  {rows.slice(current * MY_VOTES_PAGE_SIZE, (current + 1) * MY_VOTES_PAGE_SIZE).map(row)}
                </ul>
                <Pagination
                  total={rows.length}
                  offset={current * MY_VOTES_PAGE_SIZE}
                  limit={MY_VOTES_PAGE_SIZE}
                  onPageChange={(p) => {
                    setPage(p - 1);
                    // To the top of the list, not of the page: the summary doesn't come back each turn.
                    listTop.current?.scrollIntoView({ block: "start" });
                  }}
                />
              </>
            )}
          </div>
        </div>
      )}

      {tools}
    </div>
  );
}

function StorageNote({ storage }: { storage: Exclude<VoteStorage, "server" | "unavailable"> }) {
  if (storage === "memory") {
    return (
      <span role="status" className="text-destructive">
        On this page only. Votes won&apos;t be saved on this browser.
      </span>
    );
  }
  if (storage === "account") {
    return (
      <>
        Saved in your account. To download your data or delete your account, go to{" "}
        <Link href="/settings" className="underline underline-offset-2">
          Settings
        </Link>
        .
      </>
    );
  }
  return <>Kept on this device only. Your votes aren&apos;t sent anywhere.</>;
}

function NoVotes({ storage }: { storage: VoteStorage }) {
  return (
    <Card className="text-center">
      <CardContent className="py-10">
        <h2 className="text-lg font-semibold text-foreground">You haven&apos;t voted on any bills yet</h2>
        <p className="mx-auto mt-2 max-w-sm text-muted-foreground">
          {storage === "account"
            ? "Bills you vote on are listed here, and you can change or remove a vote at any time."
            : "Bills you vote on in this browser are listed here, and you can change or remove a vote at any time."}
        </p>
        <Link href="/vote" className={buttonClasses({ className: "mt-6" })}>
          Start voting
        </Link>
      </CardContent>
    </Card>
  );
}

function VotesUnavailable() {
  const backend = useVoteBackend();
  return (
    <Card>
      <CardContent className="py-8 text-center">
        <p role="alert" className="text-destructive">
          We couldn&apos;t load your votes from your account.
        </p>
        {backend.refresh && (
          <Button variant="outline" className="mt-4" onClick={backend.refresh}>
            Try again
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
