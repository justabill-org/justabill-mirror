import { parseBillFilters } from "@/lib/bill-filters";
import type { DeckCard } from "@/lib/vote-deck";

/**
 * Props for a /vote VotingSession with the default filters (Laws in the 119th Congress) whose
 * rendered first batch is `cards`, the whole list: the deck deals them without reading the API.
 */
export function deckOf(cards: DeckCard[]) {
  return {
    filters: parseBillFilters(new URLSearchParams()),
    current: 119,
    first: {
      items: cards.map(({ bill, summary, card }) => ({ ...bill, summary, card })),
      total: cards.length,
      offset: 0,
      limit: 20,
    },
  };
}
