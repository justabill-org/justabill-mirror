"use client";

import { useEffect, useState } from "react";
import { BillResults, BillResultsSkeleton } from "@/components/bill/bill-results";
import { listBills } from "@/lib/api";
import { useUser } from "@/lib/auth/provider";
import { type BillViewKey, parseBillView, viewStatuses } from "@/lib/bill-views";
import type { Bill, BillListParams, PaginatedResult } from "@/lib/types";

/** Fetches the signed-in user's unvoted bills; for tests. */
export type ListUnvotedBills = (params: BillListParams, token: string) => Promise<PaginatedResult<Bill>>;

const listUnvoted: ListUnvotedBills = (params, token) => listBills({ ...params, unvoted: true }, token);

export interface UnvotedBillsProps {
  /** The page's filters other than the view; `unvoted` and the view's statuses are set here. */
  params: Omit<BillListParams, "unvoted" | "status" | "status_mode">;
  /** The /bills view (#666), whose statuses the list keeps. */
  view: BillViewKey;
  /** The server-rendered list, shown to anyone who isn't signed in. */
  children: React.ReactNode;
  /** The same filters on /vote, for the list's "Vote on these" link. */
  voteHref?: string;
  /** For tests. */
  list?: ListUnvotedBills;
}

/**
 * The /bills list under "Unvoted only" (#556). The server renders the page with no user, so the
 * signed-in user's list is fetched here with their ID token, and never cached (`listBills`).
 * Anyone else gets the server's list: the filter needs an account's votes, and BillListControls hides it.
 */
export function UnvotedBills({ params, view, children, voteHref, list = listUnvoted }: UnvotedBillsProps) {
  const { status, getIdToken } = useUser();
  const signedIn = status === "signed-in";
  // The filters as one value, so a new params object with the same filters doesn't fetch again.
  const query = JSON.stringify({ ...params, view });
  // Each result is kept with the filters it was fetched for, so a change shows the skeleton.
  const [loaded, setLoaded] = useState<{ query: string; result: PaginatedResult<Bill> } | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  // Bumped by Try again.
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    if (!signedIn) return;
    let cancelled = false;
    const { view: key, ...filters } = JSON.parse(query) as BillListParams & { view: BillViewKey };
    const { offset = 0, limit = 12 } = filters;
    // One call per view, with all its statuses (#712), like the server's list.
    getIdToken()
      .then((token) => list({ ...filters, status: viewStatuses(parseBillView(key)), offset, limit }, token))
      .then((result) => {
        if (cancelled) return;
        setLoaded({ query, result });
        // A later fetch for filters that failed before clears the failure.
        setFailed((prev) => (prev === query ? null : prev));
      })
      .catch((err: unknown) => {
        console.error("Failed to load unvoted bills:", err);
        if (!cancelled) setFailed(query);
      });
    return () => {
      cancelled = true;
    };
  }, [signedIn, getIdToken, list, query, attempt]);

  if (status === "loading") return <BillResultsSkeleton />;
  if (!signedIn) return <>{children}</>;

  if (failed === query) {
    return (
      <p role="status" className="text-sm text-destructive">
        We couldn&apos;t load the bills you haven&apos;t voted on.{" "}
        <button
          type="button"
          className="underline underline-offset-2"
          onClick={() => {
            setFailed(null);
            setAttempt((n) => n + 1);
          }}
        >
          Try again
        </button>
      </p>
    );
  }
  if (loaded?.query !== query) return <BillResultsSkeleton />;

  return (
    <BillResults
      result={loaded.result}
      emptyHint="You've voted on every bill that matches these filters."
      voteHref={voteHref}
    />
  );
}
