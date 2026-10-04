"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import type { UseUser } from "@/lib/auth/provider";
import { congressLabel } from "@/lib/congress";
import { FOLLOW_API, type FollowApi } from "@/lib/follows";
import { billLabelFromId } from "@/lib/graph";
import { MAX_OFFSET } from "@/lib/paging";
import type { PaginatedResult, UserFavorite } from "@/lib/types";
import { formatDate } from "@/lib/utils";

/** Followed bills per page. */
export const FOLLOWED_PAGE_SIZE = 20;

export interface FollowedBillsProps {
  user: Pick<UseUser, "getIdToken">;
  /** For tests. */
  api?: FollowApi;
}

function congressOf(billId: string): number | null {
  const congress = Number(billId.split("-")[1]);
  return Number.isInteger(congress) && congress > 0 ? congress : null;
}

/** "Followed bills" in settings (#282): newest first, paged, each with an Unfollow control. */
export function FollowedBills({ user, api = FOLLOW_API }: FollowedBillsProps) {
  const { getIdToken } = user;
  const [offset, setOffset] = useState(0);
  const [page, setPage] = useState<PaginatedResult<UserFavorite> | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Bumped to load the current page again: after an unfollow, or on Try again.
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let cancelled = false;
    getIdToken()
      .then((token) => api.getMyFavorites(token, { offset, limit: FOLLOWED_PAGE_SIZE }))
      .then((result) => {
        if (cancelled) return;
        // Unfollowing the last bill on a later page leaves it empty: show the page before it.
        if (result.items.length === 0 && offset > 0) {
          setOffset(Math.max(0, offset - FOLLOWED_PAGE_SIZE));
          return;
        }
        setLoadFailed(false);
        setPage(result);
      })
      .catch((err: unknown) => {
        console.error("Failed to load followed bills:", err);
        if (!cancelled) setLoadFailed(true);
      });
    return () => {
      cancelled = true;
    };
  }, [api, getIdToken, offset, attempt]);

  const retry = () => {
    setLoadFailed(false);
    setPage(null);
    setAttempt((n) => n + 1);
  };

  const unfollow = async (billId: string) => {
    setRemoving(billId);
    setError(null);
    try {
      await api.removeFavorite(await getIdToken(), billId);
      setAttempt((n) => n + 1);
    } catch (err) {
      console.error("Failed to unfollow a bill:", err);
      setError(`We couldn't unfollow ${billLabelFromId(billId)}. Please try again.`);
    } finally {
      setRemoving(null);
    }
  };

  const hasNext = page !== null && offset + page.items.length < page.total && offset + FOLLOWED_PAGE_SIZE <= MAX_OFFSET;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-lg">Followed bills</CardTitle>
        <CardDescription>Bills you follow, newest first. Press Follow on a bill&apos;s page to add it.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {loadFailed ? (
          <p role="status" className="text-sm text-destructive">
            We couldn&apos;t load the bills you follow.{" "}
            <button type="button" className="underline underline-offset-2" onClick={retry}>
              Try again
            </button>
          </p>
        ) : page === null ? (
          <Skeleton className="h-16 w-full" aria-label="Loading followed bills" />
        ) : page.items.length === 0 ? (
          <p className="text-sm text-muted-foreground">You don&apos;t follow any bills yet.</p>
        ) : (
          <ul className="divide-y divide-border">
            {page.items.map((f) => {
              const congress = congressOf(f.bill_id);
              const label = billLabelFromId(f.bill_id);
              return (
                <li key={f.bill_id} className="flex flex-wrap items-center justify-between gap-3 py-3">
                  <div>
                    <Link href={`/bills/${f.bill_id}`} className="font-medium text-foreground hover:underline">
                      {label}
                    </Link>
                    <p className="text-sm text-muted-foreground">
                      {congress !== null && <>{congressLabel(congress)} · </>}
                      Followed {formatDate(f.created_at)}
                    </p>
                  </div>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => void unfollow(f.bill_id)}
                    disabled={removing !== null}
                    aria-label={`Unfollow ${label}`}
                  >
                    {removing === f.bill_id ? "Unfollowing..." : "Unfollow"}
                  </Button>
                </li>
              );
            })}
          </ul>
        )}

        {page !== null && (offset > 0 || hasNext) && (
          <nav aria-label="Followed bills pages" className="flex justify-between gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - FOLLOWED_PAGE_SIZE))}
            >
              Previous
            </Button>
            <Button variant="outline" size="sm" disabled={!hasNext} onClick={() => setOffset(offset + FOLLOWED_PAGE_SIZE)}>
              Next
            </Button>
          </nav>
        )}

        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
      </CardContent>
    </Card>
  );
}
