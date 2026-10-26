"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { type MouseEvent, useCallback, useEffect, useId, useRef, useState } from "react";
import { type BillFilterChange, BillFilterFields } from "@/components/bill/bill-filter-fields";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { accountsEnabled } from "@/lib/accounts";
import { useUser } from "@/lib/auth/provider";
import { billFiltersSearchParams, parseBillFilters } from "@/lib/bill-filters";
import { clampSearch } from "@/lib/paging";
import { BILL_VIEWS, type BillViewKey } from "@/lib/bill-views";
import { currentCongress } from "@/lib/congress";
import type { Congress } from "@/lib/types";

// The controls on /bills (#666), as one bar with the page's title (#717): the quick views with
// their counts, a search box, and the sort and the less used filters (policy area, congress, type,
// chamber) behind "Filters". The filters come from the URL through lib/bill-filters, as on /vote (#785).

/** How long the search box waits after a keystroke before it searches. */
const SEARCH_DELAY_MS = 300;

interface BillListControlsProps {
  congresses: Congress[];
  /** Each view's number of bills under the other filters; missing when it couldn't be counted. */
  counts: Partial<Record<BillViewKey, number>>;
  /** Every policy area, A to Z; null (the default) when they couldn't be read, which leaves the select out. */
  policyAreas?: readonly string[] | null;
}

export function BillListControls({ congresses, counts, policyAreas = null }: BillListControlsProps) {
  const router = useRouter();
  const searchParams = useSearchParams();
  const { status } = useUser();
  const searchId = useId();
  const panelId = useId();
  const [showMore, setShowMore] = useState(false);
  const viewsRow = useRef<HTMLUListElement>(null);
  // The URL's query as a string, which stays the same between renders until the URL changes.
  const query = searchParams.toString();

  const filters = parseBillFilters(new URLSearchParams(query));
  const view = filters.view;
  // The list shows the current congress unless `?congress=` names another, or `all` (#717).
  const current = currentCongress(congresses)?.number;
  // Without the congresses (#968) every congress is the default, so only a numbered one is a filter.
  const otherCongress =
    current === undefined
      ? typeof filters.congress === "number"
      : filters.congress !== undefined && filters.congress !== current;
  const showUnvoted = accountsEnabled() && status === "signed-in";
  const unvoted = showUnvoted && searchParams.get("unvoted") === "true";
  const moreCount = [otherCongress, filters.type, filters.chamber, filters.area, unvoted].filter(Boolean).length;

  // The URL for a change: the filters written the one way both pages read them, back to the first
  // page, keeping the page size and "Unvoted only".
  const href = useCallback(
    (change: BillFilterChange) => {
      const { unvoted: nextUnvoted, ...rest } = change;
      const url = new URLSearchParams(query);
      const params = billFiltersSearchParams({ ...parseBillFilters(url), ...rest });
      const limit = url.get("limit");
      if (limit) params.set("limit", limit);
      if (nextUnvoted ?? url.get("unvoted") === "true") params.set("unvoted", "true");
      const next = params.toString();
      return next ? `/bills?${next}` : "/bills";
    },
    [query],
  );

  // What's in the search box, and the search it waits to run (#1012). A new URL (Back) puts its
  // search back in the box (#870's pattern, as on /vote), unless the box already says it or is
  // waiting to search: then it's ahead of the URL, as when its last search lands after more typing.
  const [text, setText] = useState(filters.q ?? "");
  const [urlQ, setUrlQ] = useState(filters.q);
  const [pending, setPending] = useState<{ q: string | undefined } | null>(null);
  if (filters.q !== urlQ) {
    setUrlQ(filters.q);
    if (!pending && clampSearch(text) !== filters.q) setText(filters.q ?? "");
  }
  // The waiting search runs from an effect, so it builds its URL from the URL of the moment (a view
  // chosen meanwhile stays chosen), and leaving the page cancels it.
  useEffect(() => {
    if (!pending) return;
    const timer = setTimeout(() => {
      setPending(null);
      router.push(href(pending));
    }, SEARCH_DELAY_MS);
    return () => clearTimeout(timer);
  }, [pending, href, router]);

  // Any other change takes the waiting search with it, in one navigation, instead of the search
  // running after it and undoing it.
  const update = (change: BillFilterChange) => {
    setPending(null);
    router.push(href({ ...pending, ...change }));
  };
  // A view's link is a plain link, unless a search is waiting: then a plain click goes through
  // `update`. A click to open it elsewhere (a new tab) is left to the browser.
  const pickView = (e: MouseEvent<HTMLAnchorElement>, view: BillViewKey) => {
    if (!pending || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    update({ view });
  };

  // On a phone the chosen view can be past the edge of its row: scroll the row (not the page) to it.
  useEffect(() => {
    const row = viewsRow.current;
    const active = row?.querySelector<HTMLElement>('[aria-current="true"]');
    if (!row || !active || row.scrollWidth <= row.clientWidth) return;
    row.scrollLeft = active.offsetLeft - (row.clientWidth - active.offsetWidth) / 2;
  }, [view]);

  // A fragment, not a wrapper: the sticky bar needs the page, which holds the list, as its
  // parent, or it would scroll away with the controls. The filter panel stays out of the bar so a
  // tall panel on a phone scrolls with the page.
  return (
    <>
      {/* One bar (#717): the page's title, the views, the search and the filters. It's one row
          from xl up; below that the views take a second row, which scrolls sideways on a phone. */}
      <div className="sticky top-16 z-40 -mx-4 mb-4 border-b border-border bg-background/95 px-4 py-2 backdrop-blur sm:-mx-6 sm:mb-6 sm:px-6 sm:py-3 lg:-mx-8 lg:px-8">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 xl:flex-nowrap xl:gap-x-4">
          <h1 className="text-xl font-semibold tracking-tight text-foreground sm:text-2xl">Bills</h1>

          <div className="relative min-w-0 flex-1 xl:order-2">
            <label htmlFor={searchId} className="sr-only">
              Search bills
            </label>
            <SearchIcon className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              id={searchId}
              type="search"
              placeholder="Search bills"
              className="h-10 pl-9"
              value={text}
              onChange={(e) => {
                const value = e.target.value;
                setText(value);
                setPending({ q: value || undefined });
              }}
            />
          </div>

          <Button
            variant={showMore ? "secondary" : "outline"}
            onClick={() => setShowMore(!showMore)}
            aria-expanded={showMore}
            aria-controls={showMore ? panelId : undefined}
            className="shrink-0 xl:order-3"
          >
            Filters
            {moreCount > 0 && (
              <span className="ml-2 flex h-5 w-5 items-center justify-center rounded-full bg-primary text-xs text-primary-foreground">
                {moreCount}
                <span className="sr-only"> active</span>
              </span>
            )}
          </Button>
          {/* After the search in the markup, so a keyboard reaches the search first; from xl up it
              sits before it. The row is `relative` so the counts' sr-only text scrolls with it instead of widening
              the page. */}
          <nav aria-label="Bill views" className="-mx-4 w-screen min-w-0 sm:mx-0 sm:w-full xl:order-1 xl:w-auto">
            <ul
              ref={viewsRow}
              className="relative flex snap-x scroll-px-4 gap-2 overflow-x-auto px-4 [scrollbar-width:none] sm:flex-wrap sm:overflow-visible sm:px-0 xl:flex-nowrap"
            >
              {BILL_VIEWS.map((v) => {
                const active = v.key === view;
                const count = counts[v.key];
                return (
                  <li key={v.key} className="shrink-0 snap-start">
                    <Link
                      href={href({ view: v.key })}
                      onClick={(e) => pickView(e, v.key)}
                      aria-current={active ? "true" : undefined}
                      className={`flex min-h-10 items-center gap-1.5 whitespace-nowrap rounded-full border px-3 py-1 text-sm font-medium transition-colors ${
                        active
                          ? "border-primary bg-primary text-primary-foreground"
                          : "border-border bg-background text-foreground hover:bg-muted"
                      }`}
                    >
                      {v.label}
                      {count !== undefined && (
                        <span
                          className={`rounded-full px-1.5 py-0.5 text-xs tabular-nums ${
                            active ? "ring-1 ring-primary-foreground/50" : "bg-muted text-muted-foreground"
                          }`}
                        >
                          <span className="sr-only">, </span>
                          {count.toLocaleString("en-US")}
                          <span className="sr-only"> bills</span>
                        </span>
                      )}
                    </Link>
                  </li>
                );
              })}
            </ul>
          </nav>

        </div>
      </div>

      {showMore && (
        <div id={panelId} className="mb-4 grid gap-4 rounded-lg border border-border bg-card p-4 sm:mb-6 sm:grid-cols-2 lg:grid-cols-4">
          <BillFilterFields
            filters={filters}
            congresses={congresses}
            policyAreas={policyAreas}
            current={current}
            onChange={update}
            showUnvoted={showUnvoted}
            unvoted={unvoted}
          />
          {moreCount > 0 && (
            <div className="sm:col-span-2 lg:col-span-4">
              <Button
                variant="ghost"
                size="sm"
                onClick={() =>
                  update({ congress: undefined, type: undefined, chamber: undefined, area: undefined, unvoted: false })
                }
              >
                Clear filters
              </Button>
            </div>
          )}
        </div>
      )}
    </>
  );
}

function SearchIcon({ className }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2} aria-hidden="true">
      <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-5.197-5.197m0 0A7.5 7.5 0 105.196 5.196a7.5 7.5 0 0010.607 10.607z" />
    </svg>
  );
}
