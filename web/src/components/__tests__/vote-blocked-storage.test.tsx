// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { Component, type ReactNode } from "react";
import { act, cleanup, render, screen } from "@testing-library/react";
import type { Congress, PaginatedResult } from "@/lib/types";
import type { DeckItem } from "@/lib/vote-deck";

// #870 review: a browser that blocks site data throws on `window.localStorage` itself (Chrome's
// "Block all cookies", Firefox with cookies off: SecurityError). The vote store treats that as
// memory-only storage; /vote must still deal cards and say votes won't be saved.

vi.mock("next/navigation", () => ({
  usePathname: () => "/vote",
  useRouter: () => ({ push: vi.fn() }),
  useSearchParams: () => new URLSearchParams(""),
}));
vi.mock("@/lib/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api")>()),
  listBills: vi.fn(),
  listBillsWithCards: vi.fn(),
}));

const { FilteredDeck } = await import("@/app/(app)/vote/filtered-deck");
const { billList } = await import("@/lib/examples");

const congresses: Congress[] = [{ number: 119, start_date: "2025-01-03", is_current: true }];
const first: PaginatedResult<DeckItem> = {
  items: [{ ...billList.items[0], id: "hr-119-1", title: "Bill 1", summary: null, card: null }],
  total: 1,
  offset: 0,
  limit: 20,
};

/** Stands in for the route's error.tsx. */
class Boundary extends Component<{ children: ReactNode }, { error: unknown }> {
  state = { error: null as unknown };
  static getDerivedStateFromError(error: unknown) {
    return { error };
  }
  render() {
    return this.state.error ? <p>error.tsx: {String(this.state.error)}</p> : this.props.children;
  }
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("/vote with site data blocked", () => {
  it("deals the deck in memory instead of failing the page", async () => {
    vi.spyOn(window, "localStorage", "get").mockImplementation(() => {
      throw new DOMException("Access is denied for this document.", "SecurityError");
    });
    await act(async () => {
      render(
        <Boundary>
          <FilteredDeck congresses={congresses} policyAreas={null} first={first} />
        </Boundary>
      );
    });
    expect(screen.queryByText(/^error\.tsx/)).toBeNull();
    expect(screen.getByRole("heading", { level: 2, name: "Bill 1" })).toBeTruthy();
    expect(screen.getByText("Votes won't be saved on this browser.")).toBeTruthy();
  });
});
