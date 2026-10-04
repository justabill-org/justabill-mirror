// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { Congress, PaginatedResult, BillCardItem } from "@/lib/types";

// #870 review: typing a search on /vote. The search box must keep focus (and what was typed) when
// the debounced search lands in the URL, as /bills' box does.

let search = "";
let rerenderPage: () => void = () => {};
const push = vi.fn((href: string) => {
  search = href.includes("?") ? href.slice(href.indexOf("?") + 1) : "";
  rerenderPage();
});
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

const { FilteredDeck } = await import("@/app/(app)/vote/filtered-deck");

const congresses: Congress[] = [{ number: 119, start_date: "2025-01-03", is_current: true }];

afterEach(() => {
  cleanup();
  search = "";
  push.mockClear();
  vi.useRealTimers();
});

describe("/vote's search box", () => {
  it("keeps focus and the typed text when the search reaches the URL", () => {
    vi.useFakeTimers();
    const ui = () => <FilteredDeck congresses={congresses} policyAreas={null} />;
    const { rerender } = render(ui());
    rerenderPage = () => rerender(ui());
    fireEvent.click(screen.getByRole("button", { name: /^Filter:/ }));

    const box = screen.getByLabelText("Search bills") as HTMLInputElement;
    box.focus();
    fireEvent.change(box, { target: { value: "water " } });
    act(() => vi.advanceTimersByTime(300));
    expect(push).toHaveBeenCalledTimes(1);

    // The visitor is still typing "water rights": the box they typed in must still be there.
    const now = screen.getByLabelText("Search bills") as HTMLInputElement;
    expect(document.activeElement?.tagName).toBe("INPUT");
    expect(now.value).toBe("water ");
    expect(now).toBe(box);
  });

  it("keeps a view picked while the search was still waiting", () => {
    vi.useFakeTimers();
    const ui = () => <FilteredDeck congresses={congresses} policyAreas={null} />;
    const { rerender } = render(ui());
    rerenderPage = () => rerender(ui());
    fireEvent.click(screen.getByRole("button", { name: /^Filter:/ }));
    fireEvent.change(screen.getByLabelText("Search bills"), { target: { value: "tax" } });
    fireEvent.click(screen.getByRole("radio", { name: "Passed a chamber" }));
    expect(push).toHaveBeenLastCalledWith("/vote?show=passed", { scroll: false });
    act(() => vi.advanceTimersByTime(300));
    expect(push).toHaveBeenLastCalledWith("/vote?show=passed&q=tax", { scroll: false });
  });
});
