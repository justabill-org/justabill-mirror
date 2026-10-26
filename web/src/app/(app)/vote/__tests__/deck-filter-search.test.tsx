// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { BillCardItem, Congress, PaginatedResult } from "@/lib/types";

// #1043: /vote's search waits 300 ms after the typing. That wait mustn't outlive the page, nor
// undo Clear filters, and a search of its own landing late mustn't reset what was typed since.

let search = "";
const push = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", () => ({
  usePathname: () => "/vote",
  useRouter: () => ({ push }),
  useSearchParams: () => new URLSearchParams(search),
}));
vi.mock("@/lib/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api")>()),
  listBills: vi.fn(),
  listBillsWithCards: vi.fn(
    async (): Promise<PaginatedResult<BillCardItem>> => ({ items: [], total: 0, offset: 0, limit: 20 })
  ),
}));

const { FilteredDeck } = await import("../filtered-deck");

const congresses: Congress[] = [{ number: 119, start_date: "2025-01-03", is_current: true }];
const ui = () => <FilteredDeck congresses={congresses} policyAreas={null} />;
const searchBox = () => screen.getByLabelText("Search bills") as HTMLInputElement;

/** Renders /vote at `?query` with the filter open and fake timers on. */
function renderOpen(query = "") {
  vi.useFakeTimers();
  search = query;
  const view = render(ui());
  fireEvent.click(screen.getByRole("button", { name: /^Filter:/ }));
  return view;
}

afterEach(() => {
  cleanup();
  search = "";
  push.mockReset();
  vi.useRealTimers();
});

describe("/vote's waiting search", () => {
  it("doesn't navigate once the page is left", () => {
    const { unmount } = renderOpen();
    fireEvent.change(searchBox(), { target: { value: "water" } });
    act(() => vi.advanceTimersByTime(299));
    unmount();
    act(() => vi.advanceTimersByTime(1000));
    expect(push).not.toHaveBeenCalled();
  });

  it("still searches when the panel closes with Done", () => {
    renderOpen();
    fireEvent.change(searchBox(), { target: { value: "water" } });
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    act(() => vi.advanceTimersByTime(300));
    expect(push.mock.calls).toEqual([["/vote?q=water", { scroll: false }]]);
  });

  it("is dropped by Clear filters, which empties the box", () => {
    const { rerender } = renderOpen("show=passed");
    fireEvent.change(searchBox(), { target: { value: "tax" } });
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    search = "";
    rerender(ui());
    act(() => vi.advanceTimersByTime(1000));
    expect(push.mock.calls).toEqual([["/vote", { scroll: false }]]);
    expect(searchBox().value).toBe("");
  });

  it("keeps what was typed after it when it lands late", () => {
    const { rerender } = renderOpen();
    fireEvent.change(searchBox(), { target: { value: "wat" } });
    act(() => vi.advanceTimersByTime(300));
    expect(push).toHaveBeenLastCalledWith("/vote?q=wat", { scroll: false });

    // More typing, then the URL catches up with the first search.
    fireEvent.change(searchBox(), { target: { value: "water" } });
    search = "q=wat";
    rerender(ui());
    expect(searchBox().value).toBe("water");

    act(() => vi.advanceTimersByTime(300));
    expect(push).toHaveBeenLastCalledWith("/vote?q=water", { scroll: false });
  });

  it("puts the URL's search back in the box once its own search has landed (Back)", () => {
    const { rerender } = renderOpen("q=tax");
    fireEvent.change(searchBox(), { target: { value: "water" } });
    act(() => vi.advanceTimersByTime(300));
    search = "q=water";
    rerender(ui());

    search = "q=tax";
    rerender(ui());
    expect(searchBox().value).toBe("tax");
  });
});
