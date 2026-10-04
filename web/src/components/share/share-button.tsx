"use client";

// The buttons that open the share dialog (#88): one on the bill page's vote card with a picker for
// which of the visitor's members to compare with, and one on each published cell of the bill page's
// "How Just a Bill users voted" panel (#166). The scorecard's button was removed in #894.

import { useEffect, useId, useMemo, useState } from "react";
import { Button } from "@/components/ui/button";
import type { LocalRep } from "@/lib/local/reps";
import { billCongress, loadPositions, type PositionsFetcher } from "@/lib/scorecard";
import {
  aggregatePlace,
  aggregateShareUrl,
  billShareUrl,
  isMemberId,
  isShareableCell,
  normalizePositionVote,
  type MemberPositionVote,
  type ShareVote,
} from "@/lib/share";
import { aggregateShareText, billShareText } from "@/lib/share-links";
import type { AggregateCell } from "@/lib/types";
import { useLocalReps } from "@/lib/votes/hooks";
import { AGGREGATE_SHARE_PRIVACY_NOTE, ShareDialog } from "./share-dialog";

/** Each member's recorded position on one bill, or null while it loads. */
type RecordedVotes = Readonly<Record<string, MemberPositionVote>>;

/**
 * Loads the members' positions in the bill's congress while `active`: the same public GETs the
 * scorecard makes, carrying a member ID and a congress, never a vote. A failure counts as no
 * recorded votes, so the dialog still works.
 */
function useRecordedVotes(
  active: boolean,
  members: readonly LocalRep[],
  billId: string,
  fetchPositions?: PositionsFetcher
): RecordedVotes | null {
  const [loaded, setLoaded] = useState<{ key: string; votes: RecordedVotes } | null>(null);
  const congress = billCongress(billId);
  const key = active && congress !== undefined ? `${members.map((m) => m.id).join(",")}|${billId}` : "";

  useEffect(() => {
    if (!key || congress === undefined) return;
    let cancelled = false;
    const done = (votes: RecordedVotes) => {
      if (!cancelled) setLoaded({ key, votes });
    };
    loadPositions(members, [congress], fetchPositions)
      .then((byMember) => {
        const votes: Record<string, MemberPositionVote> = {};
        for (const m of members) {
          const position = (byMember[m.id] ?? []).find((p) => p.bill_id === billId);
          votes[m.id] = normalizePositionVote(position?.vote);
        }
        done(votes);
      })
      .catch(() => done({}));
    return () => {
      cancelled = true;
    };
  }, [key, congress, members, billId, fetchPositions]);

  if (!active) return null;
  if (congress === undefined || members.length === 0) return {};
  return loaded?.key === key ? loaded.votes : null;
}

const RECORDED_LABELS: Record<Exclude<MemberPositionVote, null>, string> = {
  yea: "voted Yea",
  nay: "voted Nay",
  present: "voted Present",
  not_voting: "didn't vote",
};

interface BillShareButtonProps {
  billId: string;
  vote: ShareVote;
  /** Overrides the positions fetch, for tests. */
  fetchPositions?: PositionsFetcher;
}

/**
 * Shares the visitor's vote on a bill, optionally next to one of their members' recorded votes.
 * The picker lists the members saved in this browser (jab.reps.v1) and starts on the first one
 * with a recorded Yea or Nay, or on "just my vote" when none has one.
 */
export function BillShareButton({ billId, vote, fetchPositions }: BillShareButtonProps) {
  const reps = useLocalReps();
  const [open, setOpen] = useState(false);
  // undefined until the visitor picks: then a member ID, or null for "just my vote".
  const [picked, setPicked] = useState<string | null | undefined>(undefined);
  const members = useMemo(() => (reps?.members ?? []).filter((m) => isMemberId(m.id)), [reps]);
  const recorded = useRecordedVotes(open, members, billId, fetchPositions);
  const pickerName = useId();

  const fallback = members.find((m) => recorded?.[m.id] === "yea" || recorded?.[m.id] === "nay")?.id ?? null;
  let memberId: string | null | undefined = picked;
  if (memberId === undefined) memberId = recorded === null ? undefined : fallback;
  const path =
    memberId === undefined ? null : billShareUrl({ billId, vote, ...(memberId ? { memberId } : {}) });

  const setOpenAndReset = (next: boolean) => {
    setOpen(next);
    if (!next) setPicked(undefined);
  };

  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpenAndReset(true)}>
        Share my vote
      </Button>
      <ShareDialog
        open={open}
        onOpenChange={setOpenAndReset}
        kind="bill"
        path={path}
        text={billShareText(billId, vote)}
      >
        {members.length > 0 && (
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium text-foreground">Compare with</legend>
            {members.map((m) => {
              const position = recorded?.[m.id];
              return (
                <label key={m.id} className="flex items-center gap-2 text-sm text-foreground">
                  <input
                    type="radio"
                    name={pickerName}
                    checked={memberId === m.id}
                    disabled={recorded === null}
                    onChange={() => setPicked(m.id)}
                  />
                  <span>
                    {m.name} <span className="text-muted-foreground">({m.chamber})</span>
                    {position && <span className="text-muted-foreground"> · {RECORDED_LABELS[position]}</span>}
                  </span>
                </label>
              );
            })}
            <label className="flex items-center gap-2 text-sm text-foreground">
              <input
                type="radio"
                name={pickerName}
                checked={memberId === null}
                disabled={recorded === null}
                onChange={() => setPicked(null)}
              />
              <span>No one, just my vote</span>
            </label>
          </fieldset>
        )}
      </ShareDialog>
    </>
  );
}

/**
 * Shares one cell of the aggregates panel. Renders nothing for a held cell or one without numbers,
 * since the card route 404s for those. The link carries the bill and the cell's scope, never a vote.
 */
export function AggregateShareButton({ billId, cell }: { billId: string; cell: AggregateCell | null }) {
  const [open, setOpen] = useState(false);
  if (!isShareableCell(cell)) return null;
  const place = aggregatePlace(cell.scope_key);
  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)} aria-label={`Share how users ${place} voted`}>
        Share
      </Button>
      <ShareDialog
        open={open}
        onOpenChange={setOpen}
        kind="aggregate"
        path={aggregateShareUrl({ billId, scopeKey: cell.scope_key })}
        text={aggregateShareText(billId, cell)}
        note={AGGREGATE_SHARE_PRIVACY_NOTE}
      />
    </>
  );
}
