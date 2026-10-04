import { Suspense } from "react";
import type { Metadata } from "next";
import { Skeleton } from "@/components/ui/skeleton";
import { MyVotes } from "@/components/votes/my-votes";

export const metadata: Metadata = {
  title: "My votes | Just a Bill",
  description: "Every bill you voted on, newest first. Change or remove a vote at any time.",
};

// Static (#738): the votes are read in the browser, from this device or, signed in, the account
// (#138), so the page is the same for everyone and signed out no request carries a vote. Where each
// bill stands comes from every congress's public list, joined to the votes there too (#843). The
// list pages in the browser, which needs a Suspense boundary for the pagination's useSearchParams.
export default function MyVotesPage() {
  return (
    <div className="mx-auto max-w-5xl px-4 py-8 sm:px-6 lg:px-8">
      <div className="mb-8">
        <h1 className="text-3xl font-bold tracking-tight text-foreground">My votes</h1>
        <p className="mt-2 text-muted-foreground">
          Every bill you voted on. You can change or remove a vote at any time.
        </p>
      </div>

      <Suspense fallback={<Skeleton className="h-64 w-full rounded-xl" role="status" aria-label="Loading your votes" />}>
        <MyVotes />
      </Suspense>
    </div>
  );
}
