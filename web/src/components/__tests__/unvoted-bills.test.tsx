// @vitest-environment jsdom
import { isValidElement, type ReactElement, type ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { axeViolations } from "@/test/axe";
import { fakeAuthStore } from "@/test/fake-auth";
import { AuthProvider } from "@/lib/auth/provider";
import type { AuthStatus } from "@/lib/auth/store";
import type { Bill, BillListParams, PaginatedResult } from "@/lib/types";
import { billList } from "@/lib/examples";
import type { BillViewKey } from "@/lib/bill-views";
import { UnvotedBills, type ListUnvotedBills } from "../bill/unvoted-bills";
import BillsPage from "@/app/(app)/bills/page";

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn() }),
  useSearchParams: () => new URLSearchParams("unvoted=true"),
}));

// "Unvoted only" on /bills for a signed-in user (#556).

afterEach(() => {
  cleanup();
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

const page = (items: Bill[]): PaginatedResult<Bill> => ({ items, total: items.length, offset: 0, limit: 12 });

type Filters = Omit<BillListParams, "unvoted" | "status" | "status_mode">;

function renderList(status: AuthStatus, list: ListUnvotedBills, params: Filters = {}, billView: BillViewKey = "all") {
  const auth = fakeAuthStore(status);
  const view = render(
    <AuthProvider store={auth.store}>
      <UnvotedBills params={params} view={billView} list={list}>
        <p>Every bill</p>
      </UnvotedBills>
    </AuthProvider>
  );
  return { ...view, auth };
}

describe("UnvotedBills", () => {
  it("lists a signed-in user's unvoted bills, fetched with their ID token", async () => {
    const list = vi.fn<ListUnvotedBills>(async () => page(billList.items.slice(0, 2)));
    renderList("signed-in", list, { congress: 119, chamber: "house", offset: 12, limit: 12 });

    expect(await screen.findByText(billList.items[0].title)).toBeTruthy();
    expect(screen.queryByText("Every bill")).toBeNull();
    expect(list).toHaveBeenCalledWith({ congress: 119, chamber: "house", offset: 12, limit: 12 }, "id-token-1");
  });

  it("keeps the view: one list with all its statuses, in the page's sort (#666, #712)", async () => {
    const [law, signed] = billList.items;
    const list = vi.fn<ListUnvotedBills>(async () => page([signed, law]));
    renderList("signed-in", list, { congress: 119, sort: "latest_action", offset: 12, limit: 12 }, "laws");

    expect(await screen.findByText(law.title)).toBeTruthy();
    expect(list.mock.calls.map(([params]) => params)).toEqual([
      { congress: 119, sort: "latest_action", status: ["became_law", "signed"], offset: 12, limit: 12 },
    ]);
    // The API's order is kept.
    const titles = screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent);
    expect(titles.indexOf(signed.title)).toBeLessThan(titles.indexOf(law.title));
  });

  it("links the user's list to /vote with the same filters (#796)", async () => {
    const auth = fakeAuthStore("signed-in");
    render(
      <AuthProvider store={auth.store}>
        <UnvotedBills params={{}} view="passed" voteHref="/vote?show=passed" list={async () => page(billList.items.slice(0, 1))}>
          <p>Every bill</p>
        </UnvotedBills>
      </AuthProvider>,
    );
    const link = await screen.findByRole("link", { name: "Vote on these" });
    expect(link.getAttribute("href")).toBe("/vote?show=passed");
  });

  it("says when the user has voted on every matching bill", async () => {
    renderList("signed-in", async () => page([]));
    expect(await screen.findByText("You've voted on every bill that matches these filters.")).toBeTruthy();
  });

  it("shows the server's list to a signed-out visitor, without fetching", () => {
    const list = vi.fn<ListUnvotedBills>();
    renderList("signed-out", list);
    expect(screen.getByText("Every bill")).toBeTruthy();
    expect(list).not.toHaveBeenCalled();
  });

  it("shows the server's list when accounts are off", () => {
    const list = vi.fn<ListUnvotedBills>();
    renderList("disabled", list);
    expect(screen.getByText("Every bill")).toBeTruthy();
    expect(list).not.toHaveBeenCalled();
  });

  it("shows neither list while the session loads, then the user's once signed in", async () => {
    const list = vi.fn<ListUnvotedBills>(async () => page(billList.items.slice(0, 1)));
    const { auth } = renderList("loading", list);
    expect(screen.queryByText("Every bill")).toBeNull();
    expect(list).not.toHaveBeenCalled();

    auth.set({ status: "signed-in" });
    expect(await screen.findByText(billList.items[0].title)).toBeTruthy();
  });

  it("fetches again when the filters change", async () => {
    const list = vi.fn<ListUnvotedBills>(async () => page(billList.items.slice(0, 1)));
    const auth = fakeAuthStore("signed-in");
    const tree = (params: Filters) => (
      <AuthProvider store={auth.store}>
        <UnvotedBills params={params} view="all" list={list}>
          <p>Every bill</p>
        </UnvotedBills>
      </AuthProvider>
    );
    const { rerender } = render(tree({ congress: 119 }));
    await screen.findByText(billList.items[0].title);

    // The same filters in a new object: no new request.
    rerender(tree({ congress: 119 }));
    rerender(tree({ congress: 118 }));
    await screen.findByText(billList.items[0].title);
    expect(list.mock.calls.map(([params]) => params)).toEqual([
      { congress: 119, offset: 0, limit: 12 },
      { congress: 118, offset: 0, limit: 12 },
    ]);
  });

  it("says when the list fails to load, and tries again", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const list = vi
      .fn<ListUnvotedBills>()
      .mockRejectedValueOnce(new Error("boom"))
      .mockResolvedValueOnce(page(billList.items.slice(0, 1)));
    const { container } = renderList("signed-in", list);

    expect(await screen.findByText(/We couldn't load the bills you haven't voted on/)).toBeTruthy();
    expect(await axeViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText(billList.items[0].title)).toBeTruthy();
    expect(list).toHaveBeenCalledTimes(2);
  });

  it("shows the list when filters that failed load on a later visit", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const list = vi
      .fn<ListUnvotedBills>()
      .mockRejectedValueOnce(new Error("boom"))
      .mockResolvedValue(page(billList.items.slice(0, 1)));
    const auth = fakeAuthStore("signed-in");
    const tree = (params: Filters) => (
      <AuthProvider store={auth.store}>
        <UnvotedBills params={params} view="all" list={list}>
          <p>Every bill</p>
        </UnvotedBills>
      </AuthProvider>
    );
    const { rerender } = render(tree({ congress: 119 }));
    await screen.findByText(/We couldn't load the bills you haven't voted on/);

    rerender(tree({ congress: 118 }));
    await screen.findByText(billList.items[0].title);
    rerender(tree({ congress: 119 }));
    await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(3));
    expect(await screen.findByText(billList.items[0].title)).toBeTruthy();
    expect(screen.queryByText(/We couldn't load/)).toBeNull();
  });

  it("has no axe violations with the user's list", async () => {
    const { container } = renderList("signed-in", async () => page(billList.items.slice(0, 2)));
    await screen.findByText(billList.items[0].title);
    expect(await axeViolations(container)).toEqual([]);
  });
});

/** Every element in a server component's returned tree (without rendering async children). */
function elements(node: ReactNode): ReactElement<Record<string, unknown>>[] {
  if (Array.isArray(node)) return node.flatMap(elements);
  if (!isValidElement<Record<string, unknown>>(node)) return [];
  return [node, ...elements(node.props.children as ReactNode)];
}

describe("BillsPage", () => {
  const renderPage = (search: Record<string, string>) =>
    BillsPage({ searchParams: Promise.resolve(search) }).then(elements);

  it("wraps the server's list in UnvotedBills for ?unvoted=true when accounts are on", async () => {
    vi.stubEnv("NEXT_PUBLIC_ACCOUNTS_ENABLED", "true");
    const tree = await renderPage({ unvoted: "true", congress: "119" });
    const unvoted = tree.find((el) => el.type === UnvotedBills);
    expect(unvoted?.props.params).toMatchObject({ congress: 119 });
    expect(unvoted?.props.view).toBe("laws");
    // The server never asks for a personal list: it has no token, and the shared list is cached.
    expect(unvoted?.props.params).not.toHaveProperty("unvoted");
    const grid = elements(unvoted?.props.children as ReactNode)[0];
    expect(grid.props.params).not.toHaveProperty("unvoted");
  });

  it("renders the plain list when accounts are off, whatever the URL says", async () => {
    vi.stubEnv("NEXT_PUBLIC_ACCOUNTS_ENABLED", "false");
    const tree = await renderPage({ unvoted: "true" });
    expect(tree.some((el) => el.type === UnvotedBills)).toBe(false);
  });

  it("asks the API only for a search it takes: 8 terms at most, and no deeper than its search cap (#619)", async () => {
    vi.stubEnv("NEXT_PUBLIC_ACCOUNTS_ENABLED", "false");
    const tree = await renderPage({ show: "all", q: "one two three four five six seven eight nine", offset: "9996" });
    const grid = tree.find((el) => el.props.offset !== undefined);
    expect(grid?.props.params).toMatchObject({ q: "one two three four five six seven eight" });
    expect(grid?.props.offset).toBe(492);
  });

  it("pages a list without a search as deep as before", async () => {
    vi.stubEnv("NEXT_PUBLIC_ACCOUNTS_ENABLED", "false");
    const tree = await renderPage({ show: "all", q: "  ", offset: "9996" });
    const grid = tree.find((el) => el.props.offset !== undefined);
    expect((grid?.props.params as { q?: string }).q).toBeUndefined();
    expect(grid?.props.offset).toBe(9996);
  });

  it("pages a view of several statuses as deep as any list, now it's one call (#712)", async () => {
    vi.stubEnv("NEXT_PUBLIC_ACCOUNTS_ENABLED", "false");
    const tree = await renderPage({ offset: "240" });
    const grid = tree.find((el) => el.props.offset !== undefined);
    // Laws (two statuses) once stopped at 84, where its two lists of 100 bills ended.
    expect(grid?.props.offset).toBe(240);
  });
});
