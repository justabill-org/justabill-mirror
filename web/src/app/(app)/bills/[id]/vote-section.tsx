"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { BillShareButton } from "@/components/share/share-button";
import { VoteButtons } from "@/components/vote/vote-buttons";
import { SPAN_VOTE_CAST, SPAN_VOTE_REMOVE, traceAction } from "@/lib/obs/browser";
import { parseBillId } from "@/lib/share";
import { useVote, useVoteBackend } from "@/lib/votes/hooks";
import { isVoteCapReached, VOTE_CAP_REACHED, VOTE_FAILED, type VoteStorage } from "@/lib/votes/backend";
import type { UserVoteChoice } from "@/lib/types";

interface VoteSectionProps {
  billId: string;
  billTitle: string;
}

const STORAGE_NOTES: Partial<Record<VoteStorage, string>> = {
  device: "Kept on this device only. Your vote isn't sent anywhere.",
  memory: "Votes won't be saved on this browser.",
  unavailable: "We couldn't load your votes from your account.",
};

// Notes that report a problem: announced, and shown in the error color.
const WARNINGS: ReadonlySet<VoteStorage> = new Set(["memory", "unavailable"]);

/** What a failed removal says (#593); Try again removes it again. */
const REMOVE_FAILED = "Your vote couldn't be removed.";
const VOTE_REMOVED = "Your vote was removed.";

export function VoteSection({ billId, billTitle }: VoteSectionProps) {
  const backend = useVoteBackend();
  const { vote, storage } = useVote(billId);
  // The choice whose save failed, or "remove" when removing the vote failed, kept so Try again
  // does it again. The buttons fall back to the stored vote, so a failed click never looks saved.
  const [failed, setFailed] = useState<UserVoteChoice | "remove" | null>(null);
  // The save was refused for the daily vote cap (#646): retrying can't work, so there's no Try again.
  const [capped, setCapped] = useState(false);
  const [saving, setSaving] = useState(false);
  const [removed, setRemoved] = useState(false);
  // The Remove button goes away with the vote, so focus moves to the note that says it's gone.
  const removedNote = useRef<HTMLParagraphElement>(null);

  useEffect(() => {
    if (removed) removedNote.current?.focus();
  }, [removed]);

  const handleVote = async (choice: UserVoteChoice) => {
    setFailed(null);
    setCapped(false);
    setRemoved(false);
    setSaving(true);
    try {
      await traceAction(SPAN_VOTE_CAST, () => backend.setVote(billId, choice, billTitle));
    } catch (err) {
      if (isVoteCapReached(err)) setCapped(true);
      else setFailed(choice);
    } finally {
      setSaving(false);
    }
  };

  const handleRemove = async () => {
    setFailed(null);
    setCapped(false);
    setSaving(true);
    try {
      await traceAction(SPAN_VOTE_REMOVE, () => backend.clearVote(billId));
      setRemoved(true);
    } catch {
      setFailed("remove");
    } finally {
      setSaving(false);
    }
  };

  const note = STORAGE_NOTES[storage];
  const warning = WARNINGS.has(storage);
  // A skip has nothing to share (#88).
  const shareable = (vote?.vote === "yea" || vote?.vote === "nay") && parseBillId(billId) ? vote.vote : null;

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-lg">Your Vote</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <VoteButtons billId={billId} currentVote={vote?.vote} onVote={handleVote} size="md" disabled={saving || storage === "server"} />
        {vote && (
          <div className="flex flex-wrap items-center gap-2">
            {shareable && <BillShareButton billId={billId} vote={shareable} />}
            <Button variant="ghost" size="sm" onClick={handleRemove} disabled={saving}>
              Remove my vote
            </Button>
            <Link href="/my-votes" className="text-sm font-medium text-foreground underline underline-offset-2">
              See all my votes
            </Link>
          </div>
        )}
        {removed && !vote && (
          <p ref={removedNote} tabIndex={-1} role="status" className="text-sm text-muted-foreground focus:outline-none">
            {VOTE_REMOVED}
          </p>
        )}
        {note && (
          <p role={warning ? "status" : undefined} className={warning ? "text-sm text-destructive" : "text-sm text-muted-foreground"}>
            {note}{" "}
            {storage === "unavailable" && backend.refresh && (
              <button type="button" className="underline underline-offset-2" onClick={backend.refresh}>
                Try again
              </button>
            )}
          </p>
        )}
        {capped && (
          <p role="alert" className="text-sm text-destructive">
            {VOTE_CAP_REACHED}
          </p>
        )}
        {failed && (
          <div role="alert" className="flex flex-wrap items-center gap-3">
            <p className="text-sm text-destructive">{failed === "remove" ? REMOVE_FAILED : VOTE_FAILED[failed]}</p>
            <Button variant="outline" size="sm" onClick={() => (failed === "remove" ? handleRemove() : handleVote(failed))}>
              Try again
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
