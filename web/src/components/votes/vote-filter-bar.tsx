"use client";

import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { ordinal } from "@/lib/graph";
import {
  activeFilterCount,
  MY_VOTES_STATUS_GROUP_LABELS,
  MY_VOTES_STATUS_GROUPS,
  type MyVoteFacets,
  type MyVotesQuery,
  type MyVotesStatusGroup,
} from "@/lib/my-votes";
import type { UserVoteChoice } from "@/lib/types";

const VOTE_OPTIONS: readonly { value: UserVoteChoice; label: string }[] = [
  { value: "yea", label: "Yea" },
  { value: "nay", label: "Nay" },
  { value: "skip", label: "Skipped" },
];

export interface VoteFilterBarProps {
  query: MyVotesQuery;
  facets: MyVoteFacets;
  /** Whether the bills' statuses are still loading; the status menu waits for them. */
  statusesLoading: boolean;
  onQuery: (next: MyVotesQuery) => void;
}

/**
 * My votes' one filter bar (#843): a search by title or number, and the vote, where the bill
 * stands, and the congress, each option with the number of votes it would list. On a phone the
 * three menus fold behind one "Filters" button that says how many are set; the search stays out.
 */
export function VoteFilterBar({ query, facets, statusesLoading, onQuery }: VoteFilterBarProps) {
  const id = useId();
  const [open, setOpen] = useState(false);
  const active = activeFilterCount(query);
  const congresses = Object.keys(facets.congress)
    .map(Number)
    .sort((a, b) => b - a);
  // A group with no votes at all isn't offered, unless it's the one picked.
  const groups = MY_VOTES_STATUS_GROUPS.filter((g) => g !== "unknown" || facets.status[g] > 0 || query.status === g);

  return (
    <div className="flex flex-wrap gap-2">
      <div className="min-w-0 flex-1 basis-48">
        <label htmlFor={`${id}-search`} className="sr-only">
          Search your votes by title or bill number
        </label>
        <Input
          id={`${id}-search`}
          type="search"
          placeholder="Search title or bill number"
          value={query.search}
          onChange={(e) => onQuery({ ...query, search: e.target.value })}
          autoComplete="off"
        />
      </div>
      <Button
        variant="outline"
        className="sm:hidden"
        aria-expanded={open}
        aria-controls={`${id}-menus`}
        onClick={() => setOpen((o) => !o)}
      >
        {active > 0 ? `Filters (${active})` : "Filters"}
      </Button>
      <div id={`${id}-menus`} className={`w-full gap-2 sm:flex sm:w-auto ${open ? "grid" : "hidden"}`}>
        <div>
          <label htmlFor={`${id}-vote`} className="sr-only">
            Your vote
          </label>
          <Select
            id={`${id}-vote`}
            className="sm:w-36"
            value={query.vote}
            onChange={(e) => onQuery({ ...query, vote: e.target.value as MyVotesQuery["vote"] })}
          >
            <option value="all">Any vote</option>
            {VOTE_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>{`${o.label} (${facets.vote[o.value].toLocaleString()})`}</option>
            ))}
          </Select>
        </div>
        <div>
          <label htmlFor={`${id}-status`} className="sr-only">
            Where the bill stands
          </label>
          <Select
            id={`${id}-status`}
            className="sm:w-48"
            value={query.status}
            disabled={statusesLoading}
            onChange={(e) => onQuery({ ...query, status: e.target.value as MyVotesStatusGroup | "all" })}
          >
            <option value="all">Any status</option>
            {groups.map((g) => (
              <option key={g} value={g}>
                {`${MY_VOTES_STATUS_GROUP_LABELS[g]} (${facets.status[g].toLocaleString()})`}
              </option>
            ))}
          </Select>
        </div>
        {congresses.length > 1 && (
          <div>
            <label htmlFor={`${id}-congress`} className="sr-only">
              Congress
            </label>
            <Select
              id={`${id}-congress`}
              className="sm:w-44"
              value={query.congress ?? "all"}
              onChange={(e) => onQuery({ ...query, congress: e.target.value === "all" ? null : Number(e.target.value) })}
            >
              <option value="all">Every congress</option>
              {congresses.map((c) => (
                <option key={c} value={c}>{`${ordinal(c)} Congress (${facets.congress[c].toLocaleString()})`}</option>
              ))}
            </Select>
          </div>
        )}
      </div>
    </div>
  );
}
