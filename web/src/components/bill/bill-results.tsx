import Link from "next/link";
import { BillCard, BillCardSkeleton } from "@/components/bill/bill-card";
import { buttonClasses } from "@/components/ui/button";
import { Pagination } from "@/components/ui/pagination";
import type { Bill, PaginatedResult } from "@/lib/types";

// A page of bills on /bills. A plain module, so both the server-rendered list and the signed-in
// "Unvoted only" list (unvoted-bills.tsx, in the browser) render the same markup.

interface BillResultsProps {
  result: PaginatedResult<Bill>;
  /** What an empty page says under "No bills found". */
  emptyHint?: string;
  /** The same filters on /vote, linked as "Vote on these" when the page has bills. */
  voteHref?: string;
}

export function BillResults({
  result,
  emptyHint = "Try adjusting your filters or search query.",
  voteHref,
}: BillResultsProps) {
  if (result.items.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center rounded-xl border border-dashed border-border py-16">
        <NoResultsIcon className="h-12 w-12 text-muted-foreground" />
        <h3 className="mt-4 text-lg font-semibold text-foreground">No bills found</h3>
        <p className="mt-2 text-center text-muted-foreground">{emptyHint}</p>
      </div>
    );
  }

  return (
    <>
      <div className="mb-4 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <p className="text-sm text-muted-foreground">
          Showing {result.offset + 1}-{Math.min(result.offset + result.limit, result.total)} of{" "}
          {result.total.toLocaleString()} bills
        </p>
        {voteHref && <VoteOnThese href={voteHref} />}
      </div>

      <div className="grid gap-6 sm:grid-cols-2 lg:grid-cols-3">
        {result.items.map((bill) => (
          <BillCard key={bill.id} bill={bill} />
        ))}
      </div>

      <div className="mt-8">
        <Pagination total={result.total} offset={result.offset} limit={result.limit} />
      </div>
    </>
  );
}

/** The link from a list to /vote with the same filters (#785): the same bills, one card at a time. */
export function VoteOnThese({ href }: { href: string }) {
  return (
    <Link href={href} className={buttonClasses({ variant: "outline", size: "sm" })}>
      Vote on these
    </Link>
  );
}

export function BillResultsSkeleton() {
  return (
    <>
      <div className="mb-4 h-5 w-48 animate-pulse rounded bg-muted" />
      <div className="grid gap-6 sm:grid-cols-2 lg:grid-cols-3">
        {Array.from({ length: 6 }).map((_, i) => (
          <BillCardSkeleton key={i} />
        ))}
      </div>
    </>
  );
}

function NoResultsIcon({ className }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5} aria-hidden="true">
      <path strokeLinecap="round" strokeLinejoin="round" d="M19.5 14.25v-2.625a3.375 3.375 0 00-3.375-3.375h-1.5A1.125 1.125 0 0113.5 7.125v-1.5a3.375 3.375 0 00-3.375-3.375H8.25m5.231 13.481L15 17.25m-4.5-15H5.625c-.621 0-1.125.504-1.125 1.125v16.5c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 00-9-9zm3.75 11.625a2.625 2.625 0 11-5.25 0 2.625 2.625 0 015.25 0z" />
    </svg>
  );
}
