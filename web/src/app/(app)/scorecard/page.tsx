import { Suspense } from "react";
import type { Metadata } from "next";
import { Skeleton } from "@/components/ui/skeleton";
import { LocalScorecard } from "@/components/scorecard/local-scorecard";
import { listCongresses } from "@/lib/api";
import { switchCongresses } from "@/lib/congress";
import { congresses as exampleCongresses } from "@/lib/examples";
import { isBuildPhase, orDevFixture } from "@/lib/fallback";

// Cacheable (#74): the only data is the congress list, the same for everyone. The list is cached
// for REVALIDATE.member, but the page regenerates every REVALIDATE.list seconds, so one built
// without the API gets its switch within minutes.
export const revalidate = 300; // REVALIDATE.list; segment config must be a literal

export const metadata: Metadata = {
  title: "Scorecard | Just a Bill",
  description: "See how your votes on bills compare with your House member's and senators' recorded votes.",
};

/**
 * The congresses the switch offers (#243). A failure renders error.tsx (and a failed regeneration
 * keeps the last good page); the build, which has no API, prerenders the page without a switch.
 */
async function loadOffered(): Promise<number[]> {
  try {
    return switchCongresses(await orDevFixture(listCongresses(), exampleCongresses));
  } catch (err) {
    if (!isBuildPhase()) throw err;
    console.warn("/scorecard: the API isn't reachable during the build; prerendering without the congress switch");
    return [];
  }
}

// The scorecard is built in the browser (#215) from the votes kept on this device, or the
// account's when signed in (#138). No cookies, and signed out no request carries a vote. The congress choice is read from the URL there too, so the page
// stays the same for everyone.
export default async function ScorecardPage() {
  const offered = await loadOffered();
  return (
    <div className="mx-auto max-w-6xl px-4 py-8 sm:px-6 lg:px-8">
      <div className="mb-8">
        <h1 className="text-3xl font-bold tracking-tight text-foreground">Scorecard</h1>
        <p className="mt-2 text-muted-foreground">
          How often your House member and senators voted the way you did, bill by bill.
        </p>
      </div>

      <Suspense fallback={<Skeleton className="h-64 w-full rounded-xl" role="status" aria-label="Loading your scorecard" />}>
        <LocalScorecard offered={offered} />
      </Suspense>
    </div>
  );
}
