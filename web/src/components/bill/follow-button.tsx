"use client";

import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { useUser } from "@/lib/auth/provider";
import { FOLLOW_API, isFollowing, type FollowApi } from "@/lib/follows";

export interface FollowButtonProps {
  billId: string;
  /** For tests. */
  api?: FollowApi;
}

/**
 * "Follow" on a bill page (#282). It renders only for a signed-in user: signed out, or with
 * accounts off, nothing but votes is kept, so there is no local-only following. The button shows
 * the saved state only; a failed request leaves it as it was and says so.
 */
export function FollowButton({ billId, api = FOLLOW_API }: FollowButtonProps) {
  const { status, getIdToken } = useUser();
  const signedIn = status === "signed-in";
  // null while the account's follows load.
  const [following, setFollowing] = useState<boolean | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Bumped by Try again to load the follows once more.
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    if (!signedIn) return;
    let cancelled = false;
    getIdToken()
      .then((token) => isFollowing(api, token, billId))
      .then((value) => {
        if (!cancelled) setFollowing(value);
      })
      .catch((err: unknown) => {
        console.error("Failed to load followed bills:", err);
        if (!cancelled) setLoadFailed(true);
      });
    return () => {
      cancelled = true;
    };
  }, [signedIn, api, billId, getIdToken, attempt]);

  const retry = () => {
    setLoadFailed(false);
    setFollowing(null);
    setAttempt((n) => n + 1);
  };

  if (!signedIn) return null;

  if (loadFailed) {
    return (
      <p role="status" className="text-sm text-destructive">
        We couldn&apos;t check whether you follow this bill.{" "}
        <button type="button" className="underline underline-offset-2" onClick={retry}>
          Try again
        </button>
      </p>
    );
  }

  const toggle = async () => {
    if (following === null) return;
    const next = !following;
    setSaving(true);
    setError(null);
    try {
      const token = await getIdToken();
      await (next ? api.addFavorite(token, billId) : api.removeFavorite(token, billId));
      setFollowing(next);
    } catch (err) {
      console.error("Failed to update followed bills:", err);
      setError(next ? "We couldn't follow this bill. Please try again." : "We couldn't unfollow this bill. Please try again.");
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="flex flex-wrap items-center gap-3">
      <Button
        variant={following ? "secondary" : "outline"}
        size="sm"
        aria-pressed={following ?? false}
        disabled={following === null || saving}
        onClick={() => void toggle()}
      >
        {following ? "Following" : "Follow"}
      </Button>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}
