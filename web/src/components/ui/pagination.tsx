"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { Button } from "@/components/ui/button";
import { clampSearch, maxOffsetFor, pageCount } from "@/lib/paging";

interface PaginationProps {
  total: number;
  offset: number;
  limit: number;
  /** The deepest offset the list can reach, when it's less than the API's (maxOffsetFor). */
  maxOffset?: number;
  /**
   * Turns pages in the component's own state instead of the URL's `offset` (My votes, #738): the
   * list is all in the browser, so the URL's search and the API's offset limits don't apply.
   */
  onPageChange?: (page: number) => void;
}

export function Pagination({ total, offset, limit, maxOffset, onPageChange }: PaginationProps) {
  const router = useRouter();
  const searchParams = useSearchParams();

  const currentPage = Math.floor(offset / limit) + 1;
  // The API refuses offsets past MAX_OFFSET (MAX_SEARCH_OFFSET in a search), and a list can stop
  // earlier (maxOffset), so pages beyond them aren't linked.
  const search = clampSearch(searchParams.get("q") ?? undefined);
  const reach = onPageChange ? (maxOffset ?? Infinity) : Math.min(maxOffsetFor(search), maxOffset ?? Infinity);
  const { pages, reachable: totalPages } = pageCount(total, limit, reach);
  const narrow = search ? "Narrow the search to see more." : "Narrow the list with filters to see more.";

  const goToPage = (page: number) => {
    if (onPageChange) {
      onPageChange(page);
      return;
    }
    const params = new URLSearchParams(searchParams.toString());
    const newOffset = (page - 1) * limit;

    if (newOffset === 0) {
      params.delete("offset");
    } else {
      params.set("offset", newOffset.toString());
    }

    router.push(`?${params.toString()}`);
  };

  if (totalPages <= 1) return null;

  // Generate page numbers to show
  const getPageNumbers = () => {
    const pages: (number | "...")[] = [];
    const showPages = 5;
    const halfShow = Math.floor(showPages / 2);

    let start = Math.max(1, currentPage - halfShow);
    let end = Math.min(totalPages, currentPage + halfShow);

    // Adjust if we're near the start or end
    if (currentPage <= halfShow) {
      end = Math.min(totalPages, showPages);
    }
    if (currentPage > totalPages - halfShow) {
      start = Math.max(1, totalPages - showPages + 1);
    }

    if (start > 1) {
      pages.push(1);
      if (start > 2) pages.push("...");
    }

    for (let i = start; i <= end; i++) {
      pages.push(i);
    }

    if (end < totalPages) {
      if (end < totalPages - 1) pages.push("...");
      pages.push(totalPages);
    }

    return pages;
  };

  return (
    <div className="flex flex-col items-center gap-2">
      <nav className="flex items-center justify-center gap-1" aria-label="Pagination">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => goToPage(currentPage - 1)}
          disabled={currentPage === 1}
          aria-label="Previous page"
        >
          <ChevronLeftIcon className="h-4 w-4" />
        </Button>

        {/* Below sm, the numbered row doesn't fit a 320px screen (WCAG 1.4.10 Reflow, #688): show where
            the visitor is instead, and only one of the two is ever displayed (and announced). */}
        <span aria-current="page" className="whitespace-nowrap px-2 text-sm tabular-nums sm:hidden">
          {`Page ${currentPage.toLocaleString()} of ${totalPages.toLocaleString()}`}
        </span>

        <span className="hidden items-center gap-1 sm:flex">
          {getPageNumbers().map((page, index) =>
            page === "..." ? (
              <span key={`ellipsis-${index}`} className="px-2 text-muted-foreground">
                ...
              </span>
            ) : (
              <Button
                key={page}
                variant={currentPage === page ? "secondary" : "ghost"}
                size="sm"
                onClick={() => goToPage(page)}
                aria-current={currentPage === page ? "page" : undefined}
                className="min-w-9"
              >
                {page}
              </Button>
            )
          )}
        </span>

        <Button
          variant="ghost"
          size="sm"
          onClick={() => goToPage(currentPage + 1)}
          disabled={currentPage >= totalPages}
          aria-label="Next page"
        >
          <ChevronRightIcon className="h-4 w-4" />
        </Button>
      </nav>
      {pages > totalPages && (
        <p className="text-center text-sm text-muted-foreground">
          {`Showing the first ${totalPages.toLocaleString()} pages. ${narrow}`}
        </p>
      )}
    </div>
  );
}

function ChevronLeftIcon({ className }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M15.75 19.5L8.25 12l7.5-7.5" />
    </svg>
  );
}

function ChevronRightIcon({ className }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M8.25 4.5l7.5 7.5-7.5 7.5" />
    </svg>
  );
}
