import { Suspense } from "react";
import type { Metadata } from "next";
import { Skeleton } from "@/components/ui/skeleton";
import { listBillsWithCards, listCongresses, listPolicyAreas } from "@/lib/api";
import { parseBillFilters } from "@/lib/bill-filters";
import { currentCongress } from "@/lib/congress";
import {
  billCards as exampleBillCards,
  billDetail as exampleBillDetail,
  billsBecameLaw as exampleLaws,
  congresses as exampleCongresses,
} from "@/lib/examples";
import { ExperimentExposure } from "@/lib/experiments/client";
import { VOTE_AA } from "@/lib/experiments/registry";
import { isBuildPhase, orDevFixture } from "@/lib/fallback";
import type { Congress, PaginatedResult } from "@/lib/types";
import { DECK_BATCH, deckListParams, type DeckItem } from "@/lib/vote-deck";
import { FilteredDeck, VoteTitle } from "./filtered-deck";

// Cacheable (#74): the default deck's first batch (Laws in the current congress, with summaries and
// card facts, #662), the same for everyone, regenerated in the background every REVALIDATE.list
// seconds. The browser reads the filters from the URL, drops the user's own votes and reads the
// rest of the list itself (#797).
export const revalidate = 300; // REVALIDATE.list; segment config must be a literal

export const metadata: Metadata = {
  title: "Vote on bills | Just a Bill",
  description: "Read what recent laws do in plain language and say how you would have voted.",
};

// The example laws start with the one the fixtures hold a full record of, so its Details sheet
// has something to show under `next dev` and on previews.
const exampleFirst: PaginatedResult<DeckItem> = {
  ...exampleLaws,
  items: [...exampleLaws.items]
    .sort((a, b) => Number(b.id === exampleBillDetail.bill.id) - Number(a.id === exampleBillDetail.bill.id))
    .map((bill) => ({ ...bill, summary: exampleBillDetail.summary, card: exampleBillCards[bill.id] ?? null })),
};

/** What /vote starts from: the congresses, every policy area and the default deck's first batch. */
interface Deck {
  congresses: Congress[];
  policyAreas: string[] | null;
  first: PaginatedResult<DeckItem>;
}

/**
 * The congresses and the default deck's first batch. A failure renders error.tsx (and a failed
 * regeneration keeps the last good page); only `next dev` shows the example bills (#73). The
 * policy areas are optional: without them the filter leaves out their select. Null only when the
 * build prerenders the page without the API.
 */
async function loadDeck(): Promise<Deck | null> {
  try {
    const congresses = await orDevFixture(listCongresses(), exampleCongresses);
    const params = deckListParams(parseBillFilters(new URLSearchParams()), currentCongress(congresses)?.number);
    const [first, policyAreas] = await Promise.all([
      orDevFixture(listBillsWithCards({ ...params, offset: 0, limit: DECK_BATCH }), exampleFirst),
      listPolicyAreas().then(
        (r) => r.policy_areas,
        () => null
      ),
    ]);
    return { congresses, policyAreas, first };
  } catch (err) {
    if (!isBuildPhase()) throw err;
    console.warn("/vote: the API isn't reachable during the build; prerendering the unavailable state");
    return null;
  }
}

export default async function VotePage() {
  const deck = await loadDeck();

  return (
    <div className="min-h-[calc(100vh-4rem)] bg-gradient-to-b from-muted/30 to-background">
      {/* The A/A run (#695): both arms render this page, /vote/v/treatment through it. */}
      <ExperimentExposure id={VOTE_AA} />
      <div className="mx-auto max-w-2xl px-4 pb-8 pt-2 sm:pt-3">
        {/* Voting area */}
        {deck === null ? (
          <div className="space-y-4">
            <VoteTitle />
            <div className="flex flex-col items-center justify-center rounded-xl border border-dashed border-border py-16 text-center">
              <h2 className="text-lg font-semibold text-foreground">Bills aren&apos;t available right now</h2>
              <p className="mt-2 max-w-xs text-muted-foreground">Please check back in a few minutes.</p>
            </div>
          </div>
        ) : (
          <Suspense fallback={<DeckSkeleton />}>
            <FilteredDeck congresses={deck.congresses} policyAreas={deck.policyAreas} first={deck.first} />
          </Suspense>
        )}
      </div>
    </div>
  );
}

function DeckSkeleton() {
  return (
    <div className="space-y-4">
      <VoteTitle />
      <Skeleton className="h-96 w-full rounded-xl" role="status" aria-label="Loading bills" />
    </div>
  );
}
