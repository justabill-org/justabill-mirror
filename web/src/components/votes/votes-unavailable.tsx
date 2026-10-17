"use client";

import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import type { VoteStorage } from "@/lib/votes/backend";
import { useVoteBackend } from "@/lib/votes/hooks";

/**
 * "We couldn't load your votes from your account", with Try again, on /vote, My votes and the
 * scorecard. Render it with the same `key` in every state of the view, first: it stays an empty
 * live region until `storage` turns "unavailable", so a screen reader announces it then (#955).
 */
export function VotesUnavailable({ storage }: { storage: VoteStorage }) {
  const backend = useVoteBackend();
  return (
    <Alert variant="error" className="mx-auto w-fit">
      {storage === "unavailable" && (
        <>
          <p>We couldn&apos;t load your votes from your account.</p>
          {backend.refresh && (
            <Button variant="outline" size="sm" className="mt-2" onClick={backend.refresh}>
              Try again
            </Button>
          )}
        </>
      )}
    </Alert>
  );
}
