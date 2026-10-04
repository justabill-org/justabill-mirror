"use client";

import Link from "next/link";
import { useEffect, useId, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { CollapsibleCard } from "@/components/ui/collapsible-card";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { PartyIndicator } from "@/components/member/party-indicator";
import { AggregateShareButton } from "@/components/share/share-button";
import { VoteBadge } from "@/components/member/vote-badge";
import { getScopeAggregate } from "@/lib/api";
import {
  AGGREGATES_LABEL,
  METHODOLOGY_AGGREGATES,
  constituencyKeys,
  findCell,
  ownConstituency,
  pickerCells,
  scopeName,
  votersLabel,
} from "@/lib/aggregates";
import { useUser } from "@/lib/auth/provider";
import type { AggregateCell, BillAggregatesResponse, ConstituencyPosition, ScopeAggregateResponse } from "@/lib/types";
import { useLocalReps } from "@/lib/votes/hooks";
import { formatDate } from "@/lib/utils";

/**
 * "How Just a Bill users voted" (docs/design/89-aggregate-analytics.md, "Web"). The national cell
 * comes from the cached server render; the visitor's own district and state, and any constituency
 * they pick, are fetched in the browser, so the page stays the same for everyone (#74).
 */
export function UserAggregates({ billId, data }: { billId: string; data: BillAggregatesResponse }) {
  const { account } = useUser();
  const local = useLocalReps();
  const own = constituencyKeys(ownConstituency(account, local));
  const [picked, setPicked] = useState("");
  const pickerId = useId();
  const options = pickerCells(data).filter((c) => !own.includes(c.scope_key));

  return (
    <CollapsibleCard title="How Just a Bill users voted" contentClassName="space-y-6">
        <p className="text-sm text-muted-foreground">
          {AGGREGATES_LABEL}{" "}
          <Link href={METHODOLOGY_AGGREGATES} className="underline underline-offset-2 hover:text-foreground">
            How these numbers work
          </Link>
        </p>
        <section aria-labelledby={`${pickerId}-national`}>
          <h3 id={`${pickerId}-national`} className="text-sm font-semibold text-foreground">
            Nationwide
          </h3>
          <CellView cell={data.national} billId={billId} />
        </section>

        {own.map((key) => (
          <ConstituencyCard
            key={key}
            billId={billId}
            scopeKey={key}
            heading={`Your ${key.includes("-") ? "district" : "state"}: ${scopeName(key)}`}
            listed={findCell(data, key)}
          />
        ))}

        {options.length > 0 && (
          <div className="space-y-3">
            <label htmlFor={pickerId} className="text-sm font-medium text-foreground">
              {own.length > 0 ? "Another state or district" : "A state or district"}
            </label>
            <Select id={pickerId} value={picked} onChange={(e) => setPicked(e.target.value)}>
              <option value="">Choose one</option>
              {options.map((c) => (
                <option key={c.scope_key} value={c.scope_key}>
                  {scopeName(c.scope_key)}
                </option>
              ))}
            </Select>
            {picked && (
              <ConstituencyCard
                key={picked}
                billId={billId}
                scopeKey={picked}
                heading={scopeName(picked)}
                listed={findCell(data, picked)}
              />
            )}
          </div>
        )}
    </CollapsibleCard>
  );
}

type Loaded = { status: "loading" } | { status: "error" } | { status: "ready"; data: ScopeAggregateResponse };

/**
 * One state's or district's cell beside the votes of the members who held its seat. `listed` is
 * the cell from the bill's list, shown while the browser fetch runs or if it fails.
 */
function ConstituencyCard({
  billId,
  scopeKey,
  heading,
  listed,
}: {
  billId: string;
  scopeKey: string;
  heading: string;
  listed: AggregateCell | null;
}) {
  const [loaded, setLoaded] = useState<Loaded>({ status: "loading" });
  const headingId = useId();

  useEffect(() => {
    let cancelled = false;
    getScopeAggregate(billId, scopeKey)
      .then((data) => {
        if (!cancelled) setLoaded({ status: "ready", data });
      })
      .catch(() => {
        if (!cancelled) setLoaded({ status: "error" });
      });
    return () => {
      cancelled = true;
    };
  }, [billId, scopeKey]);

  const cell = loaded.status === "ready" ? loaded.data.cell : listed;
  return (
    <section aria-labelledby={headingId} className="space-y-3 border-t border-border pt-4">
      <h3 id={headingId} className="text-sm font-semibold text-foreground">
        {heading}
      </h3>
      <CellView cell={cell} billId={billId} />
      {loaded.status === "loading" && (
        <Skeleton className="h-10 w-full" role="status" aria-label={`Loading how ${scopeName(scopeKey)}'s members voted`} />
      )}
      {loaded.status === "error" && (
        <p className="text-sm text-muted-foreground">We couldn&apos;t load how this seat&apos;s members voted.</p>
      )}
      {loaded.status === "ready" && <MemberVotes members={loaded.data.members} />}
    </section>
  );
}

/**
 * A cell's numbers, "Under review" for a held cell, or "Not enough votes yet". With `billId`, a
 * published cell gets a share button (#166).
 */
export function CellView({ cell, billId }: { cell: AggregateCell | null; billId?: string }) {
  if (!cell || cell.yea_pct === null || cell.nay_pct === null) {
    return <p className="mt-1 text-sm text-muted-foreground">Not enough votes yet.</p>;
  }
  const voters = votersLabel(cell.voters_floor);
  const published = cell.published_at
    ? formatDate(cell.published_at)
    : null;
  return (
    <div className="mt-2 space-y-2">
      <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1 text-sm text-foreground">
        <span>
          <span className="font-semibold">{cell.yea_pct}%</span> Yea
        </span>
        <span>
          <span className="font-semibold">{cell.nay_pct}%</span> Nay
        </span>
        {voters && <span className="text-muted-foreground">{voters}</span>}
        {cell.status === "held" && <Badge variant="outline">Under review</Badge>}
      </div>
      {/* Decorative: the percentages above carry the numbers. */}
      <div className="flex h-2 w-full overflow-hidden rounded-full bg-muted" aria-hidden="true">
        <div className="h-full bg-vote-yea" style={{ width: `${cell.yea_pct}%` }} />
        <div className="h-full bg-vote-nay" style={{ width: `${cell.nay_pct}%` }} />
      </div>
      <p className="text-xs text-muted-foreground">
        {cell.status === "held"
          ? "These are the last published numbers. A sudden change in votes is being reviewed before we update them."
          : published && `Published ${published}.`}
      </p>
      {billId && <AggregateShareButton billId={billId} cell={cell} />}
    </div>
  );
}

/** The members who held the seat and their position on the bill, by the scorecard's rule. */
function MemberVotes({ members }: { members: ConstituencyPosition[] }) {
  if (members.length === 0) {
    return <p className="text-sm text-muted-foreground">No recorded vote on this bill from this seat yet.</p>;
  }
  return (
    <ul className="space-y-2">
      {members.map((m) => (
        <li key={`${m.member_id}-${m.vote_id}`} className="flex flex-wrap items-center gap-2 text-sm">
          <PartyIndicator party={m.party} size="sm" />
          <Link href={`/members/${m.member_id}`} className="font-medium text-foreground hover:underline">
            {m.chamber === "Senate" ? "Sen." : "Rep."} {m.first_name} {m.last_name}
          </Link>
          <VoteBadge vote={m.vote} />
          <span className="text-xs text-muted-foreground">
            {m.chamber},{" "}
            {formatDate(m.vote_date)}
          </span>
        </li>
      ))}
    </ul>
  );
}
