import { Skeleton } from "@/components/ui/skeleton";

/**
 * A skeleton page for loading.tsx files (#73). Only pages that never call notFound() get one:
 * a loading.tsx makes the page stream, and a streamed page can't send a 404 status, so the bill
 * and member pages have none.
 */
export function PageLoading({ label = "Loading…" }: { label?: string }) {
  return (
    <div className="mx-auto max-w-4xl px-4 py-8 sm:px-6 lg:px-8" role="status" aria-busy="true">
      <span className="sr-only">{label}</span>
      <Skeleton className="mx-auto h-9 w-64 max-w-full" />
      <Skeleton className="mx-auto mt-3 h-5 w-96 max-w-full" />
      <Skeleton className="mx-auto mt-8 h-96 max-w-2xl" />
    </div>
  );
}
