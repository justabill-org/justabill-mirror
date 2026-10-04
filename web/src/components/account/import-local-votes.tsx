"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { importMyVotes } from "@/lib/api";
import { useUser } from "@/lib/auth/provider";
import { localVotes } from "@/lib/votes/backend";
import { useDeviceVotes, useVoteBackend } from "@/lib/votes/hooks";
import { importLocalVotes, type ImportCall, type ImportTotals } from "@/lib/votes/import";

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

function resultText({ imported, skipped, kept }: ImportTotals): string {
  let text = `Added ${plural(imported, "vote", "votes")} to your account.`;
  if (skipped > 0) {
    text += ` ${plural(skipped, "vote was", "votes were")} left out: you'd already voted on those bills in your account, or we don't have them.`;
  }
  if (kept.length > 0) {
    text += ` ${plural(kept.length, "vote stays", "votes stay")} on this device for now, because an account can add only so many votes a day. Add ${kept.length === 1 ? "it" : "them"} again tomorrow.`;
  }
  return text;
}

export interface ImportLocalVotesProps {
  /** Shows "Not now" and is called on it (onboarding); settings leaves it out. */
  onDecline?: () => void;
  /** For tests. */
  importCall?: ImportCall;
}

/**
 * Offers to add the votes kept on this device to the signed-in account (#138). Nothing is sent
 * until the user chooses to: then each vote goes to POST /me/votes:import, the account's own votes
 * win, and the device copy is cleared, since the account holds them now. Votes the daily vote cap
 * held back stay on the device (#458).
 */
export function ImportLocalVotes({ onDecline, importCall = importMyVotes }: ImportLocalVotesProps) {
  const { getIdToken } = useUser();
  const backend = useVoteBackend();
  const votes = useDeviceVotes();
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ImportTotals | null>(null);
  const [failed, setFailed] = useState(false);
  const count = Object.keys(votes).length;

  if (result) {
    return (
      <p role="status" className="text-sm text-success">
        {resultText(result)}
      </p>
    );
  }
  if (count === 0) return null;

  const handleImport = async () => {
    setBusy(true);
    setFailed(false);
    try {
      const totals = await importLocalVotes(votes, getIdToken, importCall);
      if (totals.kept.length > 0) localVotes().keepOnly(totals.kept);
      else localVotes().clearAll();
      backend.refresh?.();
      setResult(totals);
    } catch (err) {
      console.error("Failed to import votes:", err);
      setFailed(true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-lg">Your votes on this device</CardTitle>
        <CardDescription>
          You voted on {plural(count, "bill", "bills")} before signing in. They&apos;re kept on this device only.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">
          Add them to your account to see them on every device. That sends each bill and how you voted to
          your account, then clears them from this device. Where your account already has a vote on a bill,
          that vote stays.
        </p>
        <div className="flex flex-wrap gap-2">
          <Button onClick={handleImport} disabled={busy}>
            {busy ? "Adding..." : `Add ${plural(count, "vote", "votes")} to my account`}
          </Button>
          {onDecline && (
            <Button variant="ghost" onClick={onDecline} disabled={busy}>
              Not now
            </Button>
          )}
        </div>
        {failed && (
          <p role="alert" className="text-sm text-destructive">
            We couldn&apos;t add your votes. They&apos;re still on this device. Please try again.
          </p>
        )}
      </CardContent>
    </Card>
  );
}
