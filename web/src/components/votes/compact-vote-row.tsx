"use client";

import { useEffect, useId, useRef, useState } from "react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { VoteButtons } from "@/components/vote/vote-buttons";
import { ordinal } from "@/lib/graph";
import type { LocalVote } from "@/lib/local/votes";
import { MY_VOTES_STATUS_GROUP_LABELS, type VotedBillStatus } from "@/lib/my-votes";
import { SPAN_VOTE_CAST, SPAN_VOTE_REMOVE, traceAction } from "@/lib/obs/browser";
import { parseBillId } from "@/lib/share";
import { billLabel } from "@/lib/share-links";
import { BILL_STATUS_LABELS, type UserVoteChoice } from "@/lib/types";
import { isVoteCapReached, VOTE_CAP_REACHED, VOTE_FAILED } from "@/lib/votes/backend";
import { useVoteBackend } from "@/lib/votes/hooks";

/** What a failed removal says, as on the bill page (#593); Try again removes it again. */
const REMOVE_FAILED = "Your vote couldn't be removed.";

const CHOICE_LABELS: Record<UserVoteChoice, string> = { yea: "Yea", nay: "Nay", skip: "Skipped" };

// The vote as a small mark: the word carries it, the tint only repeats it.
const CHOICE_STYLES: Record<UserVoteChoice, string> = {
  yea: "bg-vote-yea/10 text-vote-yea",
  nay: "bg-vote-nay/10 text-vote-nay",
  skip: "bg-muted text-muted-foreground",
};

/**
 * The day of a vote in the reader's own zone: unlike the API's calendar dates (formatDate, UTC), a
 * vote's time is a real instant, so an evening vote in California is that day, not the next. Rows
 * render only in the browser (the server snapshot has no votes), so there's no hydration mismatch.
 */
function votedOn(at: string): string {
  return new Date(at).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" });
}

/** Where the bill stands, in words. */
function statusText(status: VotedBillStatus): string {
  if (status === "not_passed") return MY_VOTES_STATUS_GROUP_LABELS.not_passed;
  if (status === undefined) return MY_VOTES_STATUS_GROUP_LABELS.unknown;
  return BILL_STATUS_LABELS[status];
}

/** Whether the bill reached a chamber's floor and passed, so members' votes on it are there to see. */
function passedAChamber(status: VotedBillStatus | "loading"): boolean {
  return status !== undefined && status !== "loading" && status !== "not_passed";
}

export interface CompactVoteRowProps {
  billId: string;
  /** The stored vote; undefined once it's removed. */
  vote: LocalVote | undefined;
  /** The vote this page removed (or is removing), kept so Undo can cast it again. */
  removedVote: LocalVote | undefined;
  /** Where the bill stands (undefined when it isn't known), or "loading" while the lists load. */
  status: VotedBillStatus | "loading";
  onRemoved: (billId: string, vote: LocalVote) => void;
  onRestored: (billId: string) => void;
}

/**
 * One bill on the redesigned My votes (#843): a single line on a wide screen (number, title, where
 * the bill stands, the vote, the day) and two on a phone. The vote buttons and Remove sit behind
 * Change, which opens them under the row, so a page of rows isn't a wall of buttons. Changing or
 * removing a vote swaps what the open panel shows and never changes the row's height.
 */
export function CompactVoteRow({ billId, vote, removedVote, status, onRemoved, onRestored }: CompactVoteRowProps) {
  const backend = useVoteBackend();
  const panelId = useId();
  const [open, setOpen] = useState(false);
  const [failed, setFailed] = useState<UserVoteChoice | "remove" | null>(null);
  const [capped, setCapped] = useState(false);
  const [saving, setSaving] = useState(false);
  // Focus follows the controls that replace the ones just pressed: the removed note after Remove,
  // the Change button after Undo.
  const [moveFocus, setMoveFocus] = useState<"removed" | "restored" | null>(null);
  const removedNote = useRef<HTMLParagraphElement>(null);
  const toggle = useRef<HTMLButtonElement>(null);

  const shown = vote ?? removedVote;
  const parsed = parseBillId(billId);
  const label = billLabel(billId);
  const title = shown?.title;

  useEffect(() => {
    if (moveFocus === "removed") removedNote.current?.focus();
    if (moveFocus === "restored") toggle.current?.focus();
  }, [moveFocus]);

  const handleVote = async (choice: UserVoteChoice) => {
    setFailed(null);
    setCapped(false);
    setSaving(true);
    try {
      await traceAction(SPAN_VOTE_CAST, () => backend.setVote(billId, choice, title));
      if (!vote) {
        onRestored(billId);
        setMoveFocus("restored");
      }
    } catch (err) {
      if (isVoteCapReached(err)) setCapped(true);
      else setFailed(choice);
    } finally {
      setSaving(false);
    }
  };

  const handleRemove = async () => {
    if (!vote) return;
    setFailed(null);
    setCapped(false);
    setSaving(true);
    // Kept before the store lets go of the vote, so the list never drops the row in between.
    onRemoved(billId, vote);
    try {
      await traceAction(SPAN_VOTE_REMOVE, () => backend.clearVote(billId));
      setMoveFocus("removed");
    } catch {
      onRestored(billId);
      setFailed("remove");
    } finally {
      setSaving(false);
    }
  };

  if (!shown) return null;

  return (
    <li className="py-2.5">
      <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 lg:grid-cols-[9rem_minmax(0,1fr)_9.5rem_5.25rem_6.5rem_4.75rem]">
        {/* One line of facts on a phone; from lg its parts are the row's own columns. */}
        <p className="col-start-1 row-start-1 flex flex-wrap gap-x-2 text-sm text-muted-foreground lg:contents">
          <span className="whitespace-nowrap lg:col-start-1 lg:row-start-1">
            <span className="font-medium tabular-nums text-foreground">{label}</span>
            {parsed && <span>{` · ${ordinal(parsed.congress)}`}</span>}
            {parsed && <span className="sr-only"> Congress</span>}
          </span>
          <span
            className={`lg:col-start-3 lg:row-start-1 ${status === "became_law" || status === "signed" ? "font-medium text-foreground" : ""}`}
          >
            {status === "loading" ? (
              <span className="inline-block h-4 w-24 animate-pulse rounded bg-muted align-middle" aria-hidden="true" />
            ) : (
              statusText(status)
            )}
          </span>
          <span className="whitespace-nowrap tabular-nums lg:col-start-5 lg:row-start-1">
            <span className="sr-only">Voted </span>
            {votedOn(shown.at)}
          </span>
        </p>
        <Link
          href={`/bills/${billId}`}
          title={title}
          className="col-start-1 row-start-2 line-clamp-2 font-medium text-foreground underline-offset-2 [overflow-wrap:anywhere] hover:underline lg:col-start-2 lg:row-start-1 lg:line-clamp-1"
        >
          {title ?? label}
        </Link>
        <span
          className={`col-start-2 row-start-1 justify-self-end rounded-md px-2 py-0.5 text-center text-sm font-semibold lg:col-start-4 lg:justify-self-start ${
            vote ? CHOICE_STYLES[vote.vote] : "text-muted-foreground"
          }`}
        >
          <span className="sr-only">Your vote: </span>
          {vote ? CHOICE_LABELS[vote.vote] : "Removed"}
        </span>
        <Button
          ref={toggle}
          variant="ghost"
          size="sm"
          className="col-start-2 row-start-2 min-w-[4.75rem] justify-self-end border border-border lg:col-start-6 lg:row-start-1"
          aria-expanded={open}
          aria-controls={panelId}
          aria-label={`${open ? "Done changing" : "Change"} my vote on ${label}`}
          onClick={() => setOpen((o) => !o)}
        >
          {open ? "Done" : "Change"}
        </Button>
      </div>

      <div id={panelId} hidden={!open} className="mt-2 rounded-lg bg-muted/60 px-3 py-2.5">
        {/* The same height with the buttons or with the removed note, so nothing below moves. */}
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          {/* The buttons and the removed note share one cell, the unused one invisible, so the panel
              is as tall with either and nothing below moves when a vote is removed or put back. */}
          <div className="grid items-center">
            <div className={`col-start-1 row-start-1 flex flex-wrap items-center gap-x-3 gap-y-2 ${vote ? "" : "invisible"}`}>
              <div role="group" aria-label={`Your vote on ${label}`}>
                <VoteButtons billId={billId} currentVote={vote?.vote} onVote={handleVote} size="sm" disabled={saving} />
              </div>
              <Button
                variant="ghost"
                size="sm"
                onClick={handleRemove}
                disabled={saving}
                aria-label={`Remove my vote on ${label}`}
              >
                Remove
              </Button>
            </div>
            <p
              ref={removedNote}
              tabIndex={-1}
              role="status"
              className={`col-start-1 row-start-1 text-sm text-muted-foreground focus:outline-none ${vote ? "invisible" : ""}`}
            >
              {!vote && (
                <>
                  Removed your vote on {label}.{" "}
                  <button
                    type="button"
                    className="inline-flex min-h-6 items-center font-medium text-foreground underline underline-offset-2 disabled:opacity-50"
                    onClick={() => removedVote && handleVote(removedVote.vote)}
                    disabled={saving}
                    aria-label={`Undo removing my vote on ${label}`}
                  >
                    Undo
                  </button>
                </>
              )}
            </p>
          </div>
          {/* Only a bill a chamber passed has votes by members to compare with; the link opens its Votes tab. */}
          {passedAChamber(status) && (
            <Link
              href={`/bills/${billId}#votes`}
              aria-label={`How your representatives voted on ${label}`}
              className="inline-flex min-h-6 items-center text-sm text-link underline underline-offset-2 sm:ml-auto"
            >
              How your representatives voted
            </Link>
          )}
        </div>
        {capped && (
          <p role="alert" className="mt-2 text-sm text-destructive">
            {VOTE_CAP_REACHED}
          </p>
        )}
        {failed && (
          <div role="alert" className="mt-2 flex flex-wrap items-center gap-3">
            <p className="text-sm text-destructive">{failed === "remove" ? REMOVE_FAILED : VOTE_FAILED[failed]}</p>
            <Button variant="outline" size="sm" onClick={() => (failed === "remove" ? handleRemove() : handleVote(failed))}>
              Try again
            </Button>
          </div>
        )}
      </div>
    </li>
  );
}
