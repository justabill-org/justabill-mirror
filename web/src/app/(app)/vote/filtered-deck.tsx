"use client";

import { useEffect, useRef, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { billFiltersQuery, type BillFilters, parseBillFilters } from "@/lib/bill-filters";
import { currentCongress } from "@/lib/congress";
import type { Congress, PaginatedResult } from "@/lib/types";
import { forgetOldVoteFilter, type DeckItem } from "@/lib/vote-deck";
import { DeckFilter } from "./deck-filter";
import { VotingSession } from "./voting-session";

interface FilteredDeckProps {
  congresses: Congress[];
  /** Every policy area, A to Z (`GET /policy-areas`); null when they couldn't be read. */
  policyAreas: readonly string[] | null;
  /** The default deck's first batch (Laws in the current congress), the same for everyone. */
  first?: PaginatedResult<DeckItem>;
}

/**
 * /vote's filter and the deck it picks (#797). The filters live in the URL, read the way /bills
 * reads them (lib/bill-filters), in the browser so the page stays cacheable. A choice is a new
 * history entry, so Back returns to the previous filter, and it starts a new deck.
 */
export function FilteredDeck({ congresses, policyAreas, first }: FilteredDeckProps) {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const filters = parseBillFilters(new URLSearchParams(searchParams.toString()));
  const query = billFiltersQuery(filters);
  const current = currentCongress(congresses)?.number;
  const [open, setOpen] = useState(false);

  // The filter isn't remembered on the device any more (#797): drop what an older version saved.
  useEffect(() => forgetOldVoteFilter(() => window.localStorage), []);

  // The filters of the latest render: the search box's change lands 300 ms after the typing, and
  // must add to a view picked meanwhile rather than to the filters it was typed under.
  const latest = useRef(filters);
  useEffect(() => {
    latest.current = filters;
  });

  const go = (next: BillFilters) => {
    const q = billFiltersQuery(next);
    router.push(q ? `${pathname}?${q}` : pathname, { scroll: false });
  };

  return (
    <div className="space-y-4 sm:space-y-6">
      {/* One bar (#717): the page's title and the filter. */}
      <DeckFilter
        filters={filters}
        congresses={congresses}
        current={current}
        policyAreas={policyAreas}
        onChange={(change) => go({ ...latest.current, ...change })}
        onClear={() => go(parseBillFilters(new URLSearchParams()))}
        open={open}
        onOpenChange={setOpen}
        lead={<VoteTitle />}
      />
      <VotingSession
        key={query}
        filters={filters}
        current={current}
        first={first}
        onChangeFilters={() => {
          setOpen(true);
          window.scrollTo({ top: 0 });
        }}
      />
    </div>
  );
}

/** /vote's title, at the start of its bar; the page renders it alone while the deck loads. */
export function VoteTitle() {
  return <h1 className="mr-auto text-xl font-semibold tracking-tight text-foreground sm:text-2xl">Vote</h1>;
}
