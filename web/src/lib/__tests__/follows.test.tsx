// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { axeViolations } from "@/test/axe";
import { fakeAuthStore } from "@/test/fake-auth";
import { AuthProvider } from "../auth/provider";
import { isFollowing, type FollowApi } from "../follows";
import type { AuthStatus } from "../auth/store";
import type { UserFavorite } from "../types";
import { FollowButton } from "@/components/bill/follow-button";
import { FollowedBills, FOLLOWED_PAGE_SIZE } from "@/app/(app)/settings/followed-bills";
import { YourData } from "@/app/(app)/settings/your-data";

// Following bills (#282): the Follow button on a bill page and "Followed bills" in settings, over
// a fake /me/favorites that keeps its list in memory, newest first.

function favorite(billId: string, day = 1): UserFavorite {
  return { user_id: "u-1", bill_id: billId, created_at: `2026-09-${String(day).padStart(2, "0")}T12:00:00Z` };
}

/** A fake /me/favorites; `fail` makes the named calls reject. */
function fakeApi(initial: string[] = [], fail: Partial<Record<keyof FollowApi, boolean>> = {}) {
  let ids = [...initial];
  const api = {
    getMyFavorites: vi.fn<FollowApi["getMyFavorites"]>(async (_token, { offset = 0, limit = 20 } = {}) => {
      if (fail.getMyFavorites) throw new Error("boom");
      return { items: ids.slice(offset, offset + limit).map((id) => favorite(id)), total: ids.length, offset, limit };
    }),
    addFavorite: vi.fn<FollowApi["addFavorite"]>(async (_token, billId) => {
      if (fail.addFavorite) throw new Error("boom");
      ids = [billId, ...ids.filter((id) => id !== billId)];
    }),
    removeFavorite: vi.fn<FollowApi["removeFavorite"]>(async (_token, billId) => {
      if (fail.removeFavorite) throw new Error("boom");
      ids = ids.filter((id) => id !== billId);
    }),
  };
  return { api, ids: () => ids };
}

function renderButton(status: AuthStatus, api: FollowApi) {
  return render(
    <AuthProvider store={fakeAuthStore(status).store}>
      <FollowButton billId="hr-119-1" api={api} />
    </AuthProvider>
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("isFollowing", () => {
  it("pages through the account's follows until it finds the bill", async () => {
    const ids = Array.from({ length: 150 }, (_, i) => `hr-119-${i + 2}`).concat("hr-119-1");
    const { api } = fakeApi(ids);
    expect(await isFollowing(api, "t", "hr-119-1")).toBe(true);
    expect(api.getMyFavorites).toHaveBeenCalledTimes(2);
    expect(api.getMyFavorites).toHaveBeenLastCalledWith("t", { offset: 100, limit: 100 });
  });

  it("stops at the end of the list", async () => {
    const { api } = fakeApi(["s-119-5"]);
    expect(await isFollowing(api, "t", "hr-119-1")).toBe(false);
    expect(api.getMyFavorites).toHaveBeenCalledTimes(1);
  });
});

describe("FollowButton", () => {
  it("renders nothing signed out, with accounts off, or in the server render", () => {
    for (const status of ["signed-out", "disabled", "loading"] as const) {
      const { api } = fakeApi();
      const { container } = renderButton(status, api);
      expect(container.innerHTML).toBe("");
      expect(api.getMyFavorites).not.toHaveBeenCalled();
      cleanup();
    }
    expect(renderToStaticMarkup(<FollowButton billId="hr-119-1" />)).toBe("");
  });

  it("shows whether the bill is already followed", async () => {
    renderButton("signed-in", fakeApi(["hr-119-1"]).api);
    const button = await screen.findByRole("button", { name: "Following" });
    expect(button.getAttribute("aria-pressed")).toBe("true");
  });

  it("has no axe violations", async () => {
    const { container } = renderButton("signed-in", fakeApi().api);
    await screen.findByRole("button", { name: "Follow" });
    expect(await axeViolations(container)).toEqual([]);
  });

  it("follows and unfollows without a reload", async () => {
    const { api, ids } = fakeApi();
    renderButton("signed-in", api);
    fireEvent.click(await screen.findByRole("button", { name: "Follow" }));
    const following = await screen.findByRole("button", { name: "Following" });
    expect(api.addFavorite).toHaveBeenCalledWith("id-token-1", "hr-119-1");
    expect(ids()).toEqual(["hr-119-1"]);

    fireEvent.click(following);
    await screen.findByRole("button", { name: "Follow" });
    expect(api.removeFavorite).toHaveBeenCalledWith("id-token-1", "hr-119-1");
    expect(ids()).toEqual([]);
  });

  it("keeps the previous state and says so when following fails", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    renderButton("signed-in", fakeApi([], { addFavorite: true }).api);
    fireEvent.click(await screen.findByRole("button", { name: "Follow" }));
    expect((await screen.findByRole("alert")).textContent).toContain("couldn't follow this bill");
    const button = screen.getByRole("button", { name: "Follow" });
    expect(button.getAttribute("aria-pressed")).toBe("false");
    expect(screen.queryByRole("button", { name: "Following" })).toBeNull();
  });

  it("keeps Following when unfollowing fails", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    renderButton("signed-in", fakeApi(["hr-119-1"], { removeFavorite: true }).api);
    fireEvent.click(await screen.findByRole("button", { name: "Following" }));
    expect((await screen.findByRole("alert")).textContent).toContain("couldn't unfollow this bill");
    expect(screen.getByRole("button", { name: "Following" })).toBeTruthy();
  });

  it("offers a retry when the follows can't be loaded", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const { api } = fakeApi([], { getMyFavorites: true });
    renderButton("signed-in", api);
    expect((await screen.findByRole("status")).textContent).toContain("couldn't check whether you follow");
    expect(screen.queryByRole("button", { name: /^Follow/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(api.getMyFavorites).toHaveBeenCalledTimes(2));
  });
});

describe("FollowedBills", () => {
  const user = { getIdToken: vi.fn(async () => "id-token-1") };

  it("lists followed bills newest first, each linking to its page", async () => {
    render(<FollowedBills user={user} api={fakeApi(["s-119-5", "hr-118-1"]).api} />);
    const links = await screen.findAllByRole("link");
    expect(links.map((a) => [a.textContent, a.getAttribute("href")])).toEqual([
      ["S. 5", "/bills/s-119-5"],
      ["H.R. 1", "/bills/hr-118-1"],
    ]);
    expect(screen.getByText(/2025–26 \(119th\)/)).toBeTruthy();
    expect(screen.queryByRole("navigation")).toBeNull();
  });

  it("has no axe violations", async () => {
    const { container } = render(<FollowedBills user={user} api={fakeApi(["s-119-5"]).api} />);
    await screen.findByRole("link");
    expect(await axeViolations(container)).toEqual([]);
  });

  it("says when nothing is followed", async () => {
    render(<FollowedBills user={user} api={fakeApi().api} />);
    expect(await screen.findByText(/don't follow any bills yet/)).toBeTruthy();
  });

  it("pages through the list", async () => {
    const ids = Array.from({ length: FOLLOWED_PAGE_SIZE + 3 }, (_, i) => `hr-119-${i + 1}`);
    const { api } = fakeApi(ids);
    render(<FollowedBills user={user} api={api} />);
    expect(await screen.findAllByRole("link")).toHaveLength(FOLLOWED_PAGE_SIZE);
    const nav = screen.getByRole("navigation", { name: "Followed bills pages" });
    fireEvent.click(within(nav).getByRole("button", { name: "Next" }));
    await waitFor(() => expect(screen.getAllByRole("link")).toHaveLength(3));
    expect(api.getMyFavorites).toHaveBeenLastCalledWith("id-token-1", { offset: FOLLOWED_PAGE_SIZE, limit: FOLLOWED_PAGE_SIZE });
    expect(within(nav).getByRole("button", { name: "Next" })).toHaveProperty("disabled", true);
  });

  it("unfollows a bill and refreshes the list", async () => {
    const { api, ids } = fakeApi(["s-119-5", "hr-118-1"]);
    render(<FollowedBills user={user} api={api} />);
    fireEvent.click(await screen.findByRole("button", { name: "Unfollow S. 5" }));
    await waitFor(() => expect(screen.queryByRole("link", { name: "S. 5" })).toBeNull());
    expect(ids()).toEqual(["hr-118-1"]);
  });

  it("steps back a page when unfollowing empties the last one", async () => {
    const ids = Array.from({ length: FOLLOWED_PAGE_SIZE + 1 }, (_, i) => `hr-119-${i + 1}`);
    render(<FollowedBills user={user} api={fakeApi(ids).api} />);
    await screen.findAllByRole("link");
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    const last = `Unfollow H.R. ${FOLLOWED_PAGE_SIZE + 1}`;
    fireEvent.click(await screen.findByRole("button", { name: last }));
    await waitFor(() => expect(screen.getAllByRole("link")).toHaveLength(FOLLOWED_PAGE_SIZE));
    expect(screen.queryByRole("navigation")).toBeNull();
  });

  it("keeps the bill and says so when unfollowing fails", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(<FollowedBills user={user} api={fakeApi(["s-119-5"], { removeFavorite: true }).api} />);
    fireEvent.click(await screen.findByRole("button", { name: "Unfollow S. 5" }));
    expect((await screen.findByRole("alert")).textContent).toContain("couldn't unfollow S. 5");
    expect(screen.getByRole("link", { name: "S. 5" })).toBeTruthy();
  });

  it("offers a retry when the list can't be loaded", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const { api } = fakeApi([], { getMyFavorites: true });
    render(<FollowedBills user={user} api={api} />);
    expect((await screen.findByRole("status")).textContent).toContain("couldn't load the bills you follow");
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(api.getMyFavorites).toHaveBeenCalledTimes(2));
  });
});

describe("Your data", () => {
  it("says followed bills are in the export and deleted with the account", () => {
    const user = { getIdToken: vi.fn(), reauthenticate: vi.fn(), signOut: vi.fn() };
    render(<YourData user={user} />);
    expect(screen.getByText(/keeps your votes, your district and the bills you follow/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Delete my account" }));
    expect(screen.getByText(/deletes your votes, your district and the bills you follow/)).toBeTruthy();
  });
});
