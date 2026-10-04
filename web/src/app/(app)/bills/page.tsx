import type { Metadata } from "next";
import { Suspense } from "react";
import { BillListCard, BillListCardSkeleton } from "@/components/bill/bill-list-card";
import { BillListControls } from "@/components/bill/bill-list-controls";
import { BillResults, VoteOnThese } from "@/components/bill/bill-results";
import { UnvotedBills } from "@/components/bill/unvoted-bills";
import { Pagination } from "@/components/ui/pagination";
import { accountsEnabled } from "@/lib/accounts";
import { countBills, listBillsWithSummaries, listCongresses, listPolicyAreas } from "@/lib/api";
import {
  type BillFilterParams,
  type BillFilters,
  billListParams,
  parseBillFilters,
  searchParamsOf,
  voteHref,
} from "@/lib/bill-filters";
import {
  type BillSort,
  type BillView,
  type BillViewKey,
  parseBillView,
  viewCounts,
  viewStatuses,
} from "@/lib/bill-views";
import {
  billList as exampleBillList,
  billsBecameLaw as exampleLaws,
  billsEverPassedHouse as examplePassed,
  congresses as exampleCongresses,
} from "@/lib/examples";
import { currentCongress } from "@/lib/congress";
import { orDevFixture } from "@/lib/fallback";
import { maxOffsetFor, parseLimit, parseOffset } from "@/lib/paging";
import type { Bill, BillCounts, PaginatedResult } from "@/lib/types";

const PAGE_SIZE = 12;

export const metadata: Metadata = {
  title: "Bills in Congress | Just a Bill",
  description: "Browse and search bills in Congress, with plain-language summaries and their status.",
};

interface BillsPageProps {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}

export default async function BillsPage({ searchParams }: BillsPageProps) {
  const query = searchParamsOf(await searchParams);
  // The filters are read as /vote reads them (lib/bill-filters, #785): an unknown value falls back
  // to its default, so a hand-edited URL shows a list rather than a 400.
  const filters = parseBillFilters(query);
  const view = parseBillView(filters.view);
  const sort = filters.sort;
  // Paging stays within the API's rules, so a very deep URL shows a list rather than a 400 (#207,
  // #619).
  const limit = parseLimit(query.get("limit") ?? undefined, PAGE_SIZE);
  const offset = parseOffset(query.get("offset") ?? undefined, limit, maxOffsetFor(filters.q));
  // The current congress unless the URL names another, or `all` (#717). If the congresses can't
  // be read the list still renders, across every congress.
  const current = await orDevFixture(listCongresses(), exampleCongresses).then(
    (all) => (Array.isArray(all) ? currentCongress(all)?.number : undefined),
    () => undefined,
  );
  const base = billListParams(filters, current);
  // "Unvoted only" needs the signed-in user's votes, which only the browser knows: the server
  // renders the whole list, and UnvotedBills swaps in the user's own list once they're signed in.
  const grid = (
    <ViewGrid
      view={view}
      params={base}
      sort={sort}
      offset={offset}
      limit={limit}
      visitor={isFiltered(base, current, offset, limit)}
      filters={filters}
    />
  );
  const unvoted = accountsEnabled() && query.get("unvoted") === "true";

  return (
    <div className="mx-auto max-w-7xl px-4 pb-6 sm:px-6 sm:pb-8 lg:px-8">
      <Suspense fallback={<ControlsSkeleton />}>
        <Controls base={base} />
      </Suspense>

      <Suspense key={`${view.key}-${sort}-${offset}`} fallback={<ResultsSkeleton />}>
        {unvoted ? (
          <UnvotedBills params={{ ...base, offset, limit }} view={view.key} voteHref={voteHref(filters)}>
            {grid}
          </UnvotedBills>
        ) : (
          grid
        )}
      </Suspense>
    </div>
  );
}

/**
 * Whether the list differs from a view's first page in either sort, in the current congress,
 * with no other filter. Those
 * eight lists stay cached and shared; any other (a search, a filter, a later page) is counted
 * against the visitor's own rate limit and isn't cached (#607, docs/design/607-per-visitor-web-limits.md).
 */
function isFiltered(
  { congress, type, chamber, policy_area, q }: BillFilterParams,
  current: number | undefined,
  offset: number,
  limit: number,
): boolean {
  if (offset > 0 || limit !== PAGE_SIZE || congress !== current) return true;
  return [type, chamber, policy_area, q].some((value) => value !== undefined && value !== "");
}

async function Controls({ base }: { base: BillFilterParams }) {
  const [congresses, counts, policyAreas] = await Promise.all([
    orDevFixture(listCongresses(), exampleCongresses),
    countViews(base),
    // Optional: without the areas, the panel leaves out their select and the rest works.
    listPolicyAreas().then(
      (r) => r.policy_areas,
      () => null,
    ),
  ]);
  return <BillListControls congresses={congresses} counts={counts} policyAreas={policyAreas} />;
}

/**
 * Each view's total under the other filters, from one `GET /bills/counts` call (#713). Without a
 * search the counts are few (a congress, type and chamber at most), so they stay cached and shared;
 * a search's counts are a search of their own, counted against the visitor like its list (#607).
 * When they can't be read the views show no counts.
 */
async function countViews(base: BillFilterParams): Promise<Partial<Record<BillViewKey, number>>> {
  try {
    return viewCounts(await orDevFixture(countBills(base, { visitor: !!base.q }), exampleCounts));
  } catch {
    return {};
  }
}

/** The counts under `next dev` with no API: each view's example list's total. */
const exampleCounts: BillCounts = {
  by_status: { became_law: exampleLaws.total, passed_house: examplePassed.total, in_committee: exampleBillList.total },
  total: exampleBillList.total,
};

function exampleFor(view: BillView): PaginatedResult<Bill> {
  if (view.key === "laws") return exampleLaws;
  if (view.key === "passed") return examplePassed;
  return exampleBillList;
}

interface ViewGridProps {
  view: BillView;
  /** The filters other than the view and the page. */
  params: BillFilterParams;
  sort: BillSort;
  offset: number;
  limit: number;
  /** Count the list's calls against the visitor (see isFiltered). */
  visitor: boolean;
  /** The filters as the URL gave them, for the link to /vote. */
  filters: BillFilters;
}

async function ViewGrid({ view, params, sort, offset, limit, visitor, filters }: ViewGridProps) {
  const example = exampleFor(view);
  const load = listBillsWithSummaries({ ...params, status: viewStatuses(view), offset, limit }, { visitor });
  const result = await orDevFixture(load, {
    ...example,
    items: example.items.map((b) => ({ ...b, summary: null })),
  });

  if (result.items.length === 0) {
    return <BillResults result={result} emptyHint="Try another view, or a different search." />;
  }

  return (
    <section aria-label={`${view.label}: bills`}>
      <div className="mb-3 flex flex-wrap items-center justify-between gap-x-4 gap-y-2 sm:mb-4">
        <p className="text-sm text-muted-foreground">
          {view.label}: showing {result.offset + 1}-{result.offset + result.items.length} of{" "}
          {result.total.toLocaleString("en-US")} bills
        </p>
        <VoteOnThese href={voteHref(filters)} />
      </div>
      <ul className="grid gap-3 sm:grid-cols-2 sm:gap-5 xl:grid-cols-3">
        {result.items.map((bill) => (
          <li key={bill.id}>
            <BillListCard bill={bill} sort={sort} />
          </li>
        ))}
      </ul>
      <div className="mt-8">
        <Pagination
          total={result.total}
          offset={result.offset}
          limit={limit}
          maxOffset={maxOffsetFor(params.q)}
        />
      </div>
    </section>
  );
}

/** The bar's shape while the congresses and counts load: the title is there from the start. */
function ControlsSkeleton() {
  return (
    <div className="mb-4 flex flex-wrap items-center gap-3 py-2 sm:mb-6 sm:py-3">
      <h1 className="text-xl font-semibold tracking-tight text-foreground sm:text-2xl">Bills</h1>
      <div className="h-10 min-w-0 flex-1 animate-pulse rounded-lg bg-muted" />
      <div className="h-10 w-24 animate-pulse rounded-lg bg-muted" />
    </div>
  );
}

function ResultsSkeleton() {
  return (
    <div role="status" aria-label="Loading bills">
      <div className="mb-4 h-5 w-48 animate-pulse rounded bg-muted" />
      <div className="grid gap-3 sm:grid-cols-2 sm:gap-5 xl:grid-cols-3">
        {Array.from({ length: 6 }).map((_, i) => (
          <BillListCardSkeleton key={i} />
        ))}
      </div>
    </div>
  );
}
