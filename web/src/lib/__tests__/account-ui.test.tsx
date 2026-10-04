// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { fakeAccount, fakeAuthStore } from "@/test/fake-auth";
import { ApiError } from "../api";
import type { AuthStore } from "../auth/store";
import type { VoteBackend, VoteState } from "../votes/backend";
import type { AccountExport } from "../types";

// The signed-in screens of #138: where votes go once signed in (AccountVotesProvider), the offer to
// add this device's votes, onboarding after sign-in, and "Download my data" / "Delete my account"
// in settings. The auth store and the API are faked.

const push = vi.fn();
const replace = vi.fn();
let search = "";
vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  usePathname: () => "/settings",
  useRouter: () => ({ replace, push, refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(search),
}));

const { AuthProvider } = await import("../auth/provider");
const { AccountVotesProvider } = await import("../votes/provider");
const { localVotes } = await import("../votes/backend");
const { VoteSection } = await import("@/app/(app)/bills/[id]/vote-section");
const { VotingSession } = await import("@/app/(app)/vote/voting-session");
import { deckOf } from "@/test/deck";
const { ScorecardView } = await import("@/components/scorecard/local-scorecard");
const { ImportLocalVotes } = await import("@/components/account/import-local-votes");
const { Onboarding } = await import("@/app/(auth)/signup/onboarding");
const { SignIn } = await import("@/app/(auth)/login/sign-in");
const { YourData } = await import("@/app/(app)/settings/your-data");
const { AccountSettings } = await import("@/app/(app)/settings/account-settings");
const { ONBOARDED_KEY } = await import("../auth/onboarding");

/** A signed-in vote backend with a fixed state; setVote and refresh are spies. */
function fakeAccountBackend(state: VoteState = { votes: {}, storage: "account" }) {
  return {
    subscribe: () => () => {},
    getSnapshot: () => state,
    getServerSnapshot: () => state,
    setVote: vi.fn<VoteBackend["setVote"]>(async () => {}),
    clearVote: vi.fn<VoteBackend["clearVote"]>(async () => {}),
    refresh: vi.fn(),
  } satisfies VoteBackend;
}

function withAuth(store: AuthStore, ui: React.ReactNode, accountBackend?: () => VoteBackend) {
  return render(
    <AuthProvider store={store}>
      <AccountVotesProvider accountBackend={accountBackend}>{ui}</AccountVotesProvider>
    </AuthProvider>
  );
}

function voteOnDevice(n: number) {
  for (let i = 1; i <= n; i++) localVotes().setVote(`hr-119-${i}`, "yea", `Bill ${i}`);
}

beforeEach(() => {
  localStorage.clear();
  localVotes().clearAll();
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  search = "";
});

describe("AccountVotesProvider", () => {
  it("keeps votes on this device when signed out or with accounts off", async () => {
    for (const status of ["signed-out", "disabled"] as const) {
      const backend = fakeAccountBackend();
      withAuth(fakeAuthStore(status).store, <VoteSection billId="hr-119-1" billTitle="One" />, () => backend);
      fireEvent.click(screen.getByRole("button", { name: /yea/i }));
      await waitFor(() => expect(localVotes().getSnapshot().value["hr-119-1"]?.vote).toBe("yea"));
      expect(screen.getByText(/Kept on this device only/)).toBeTruthy();
      expect(backend.setVote).not.toHaveBeenCalled();
      cleanup();
      localVotes().clearAll();
    }
  });

  it("sends votes to the account when signed in, and drops the device copy", async () => {
    const backend = fakeAccountBackend();
    const factory = vi.fn(() => backend);
    withAuth(fakeAuthStore("signed-in").store, <VoteSection billId="hr-119-1" billTitle="One" />, factory);
    fireEvent.click(screen.getByRole("button", { name: /nay/i }));
    await waitFor(() => expect(backend.setVote).toHaveBeenCalledWith("hr-119-1", "nay", "One"));
    expect(localVotes().getSnapshot().value).toEqual({});
    expect(screen.queryByText(/this device/)).toBeNull();
    expect(factory).toHaveBeenCalledTimes(1);
  });

  it("disables voting until it knows who is signed in", () => {
    withAuth(fakeAuthStore("loading").store, <VoteSection billId="hr-119-1" billTitle="One" />);
    for (const b of screen.getAllByRole("button")) expect((b as HTMLButtonElement).disabled).toBe(true);
  });

  it("switches to the account on sign-in and back to the device on sign-out", async () => {
    const fake = fakeAuthStore("signed-out");
    const backend = fakeAccountBackend();
    withAuth(fake.store, <VoteSection billId="hr-119-1" billTitle="One" />, () => backend);
    expect(screen.getByText(/Kept on this device only/)).toBeTruthy();
    fake.set({ status: "signed-in", account: fakeAccount });
    expect(screen.queryByText(/Kept on this device only/)).toBeNull();
    fake.set({ status: "signed-out", account: null });
    expect(screen.getByText(/Kept on this device only/)).toBeTruthy();
  });

  it("says the votes couldn't load when the account failed, and retries", () => {
    const fake = fakeAuthStore("error");
    withAuth(fake.store, <VoteSection billId="hr-119-1" billTitle="One" />);
    expect(screen.getByText(/couldn't load your votes/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(fake.store.retry).toHaveBeenCalled();
  });

  it("offers a retry on /vote and /scorecard when the account's votes are unavailable", () => {
    const backend = fakeAccountBackend({ votes: {}, storage: "unavailable" });
    withAuth(fakeAuthStore("signed-in").store, <VotingSession {...deckOf([])} />, () => backend);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(backend.refresh).toHaveBeenCalledTimes(1);
    cleanup();

    withAuth(
      fakeAuthStore("signed-in").store,
      <ScorecardView votes={{}} storage="unavailable" reps={null} scores={{ status: "none" }} />,
      () => backend
    );
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(backend.refresh).toHaveBeenCalledTimes(2);
  });

  it("shows the account's votes on the scorecard, pointing to My votes instead of device tools", () => {
    render(
      <ScorecardView
        votes={{ "hr-119-1": { vote: "yea", at: "2026-10-01T00:00:00Z" } }}
        storage="account"
        reps={null}
        scores={{ status: "none" }}
      />
    );
    expect(screen.getByText(/1 vote so far, saved in your account\./)).toBeTruthy();
    expect(screen.getByRole("link", { name: "My votes" }).getAttribute("href")).toBe("/my-votes");
    expect(screen.queryByRole("button", { name: "Clear my votes" })).toBeNull();
  });
});

describe("ImportLocalVotes", () => {
  function renderImport(importCall = vi.fn(async () => ({ imported: 2, skipped: 1 })), onDecline?: () => void) {
    const backend = fakeAccountBackend();
    const fake = fakeAuthStore("signed-in");
    withAuth(fake.store, <ImportLocalVotes importCall={importCall} onDecline={onDecline} />, () => backend);
    return { backend, importCall, fake };
  }

  it("shows nothing when this device has no votes", () => {
    const { importCall } = renderImport();
    expect(screen.queryByText(/Your votes on this device/)).toBeNull();
    expect(importCall).not.toHaveBeenCalled();
  });

  it("sends nothing until asked, then imports, clears the device copy and reloads the account's votes", async () => {
    voteOnDevice(3);
    const { importCall, backend } = renderImport();
    expect(screen.getByText(/voted on 3 bills before signing in/)).toBeTruthy();
    expect(importCall).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Add 3 votes to my account" }));
    await screen.findByText(/Added 2 votes to your account\. 1 vote was left out/);
    expect(importCall).toHaveBeenCalledWith("id-token-1", expect.arrayContaining([expect.objectContaining({ bill_id: "hr-119-2", vote: "yea" })]));
    expect(localVotes().getSnapshot().value).toEqual({});
    expect(backend.refresh).toHaveBeenCalled();
  });

  it("keeps the votes the daily cap held back on the device", async () => {
    voteOnDevice(3);
    renderImport(
      vi.fn(async () => ({ imported: 2, skipped: 0, capped: 1, capped_bill_ids: ["hr-119-3"] }))
    );
    fireEvent.click(screen.getByRole("button", { name: "Add 3 votes to my account" }));
    await screen.findByText(/Added 2 votes to your account\. 1 vote stays on this device for now/);
    expect(Object.keys(localVotes().getSnapshot().value)).toEqual(["hr-119-3"]);
  });

  it("keeps the votes on the device when the import fails", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    voteOnDevice(1);
    renderImport(vi.fn(async () => Promise.reject(new Error("503"))));
    fireEvent.click(screen.getByRole("button", { name: "Add 1 vote to my account" }));
    await screen.findByRole("alert");
    expect(Object.keys(localVotes().getSnapshot().value)).toEqual(["hr-119-1"]);
    vi.mocked(console.error).mockRestore();
  });

  it("offers Not now only where asked", () => {
    voteOnDevice(1);
    const onDecline = vi.fn();
    renderImport(undefined, onDecline);
    fireEvent.click(screen.getByRole("button", { name: "Not now" }));
    expect(onDecline).toHaveBeenCalled();
    cleanup();
    renderImport();
    expect(screen.queryByRole("button", { name: "Not now" })).toBeNull();
  });
});

describe("sign-in and onboarding", () => {
  it("sends a new account through onboarding, and a returning one straight to ?next=", () => {
    search = "next=/bills";
    const fresh = fakeAuthStore("signed-in", { account: { id: "u-9", created_at: "2026-10-05T00:00:00Z" } });
    withAuth(fresh.store, <SignIn />);
    expect(replace).toHaveBeenCalledWith("/signup?next=%2Fbills");
    cleanup();
    replace.mockClear();

    withAuth(fakeAuthStore("signed-in").store, <SignIn />);
    expect(replace).toHaveBeenCalledWith("/bills");
  });

  it("sends an account with a district through onboarding when this device has votes", () => {
    voteOnDevice(2);
    search = "next=/vote";
    withAuth(fakeAuthStore("signed-in").store, <SignIn />);
    expect(replace).toHaveBeenCalledWith("/signup?next=%2Fvote");
  });

  it("asks for the district and this device's votes, then continues to ?next= and remembers it", () => {
    voteOnDevice(2);
    search = "next=/scorecard";
    withAuth(fakeAuthStore("signed-in", { account: { id: "u-9", created_at: "2026-10-05T00:00:00Z" } }).store, <Onboarding />);
    expect(screen.getByText("Find your district")).toBeTruthy();
    expect(screen.getByText("You haven't set your district yet.")).toBeTruthy();
    expect(screen.getByText(/voted on 2 bills before signing in/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Not now" }));
    expect(screen.queryByText(/before signing in/)).toBeNull();
    expect(localVotes().getSnapshot().value).not.toEqual({});

    fireEvent.click(screen.getByRole("button", { name: "Continue" }));
    expect(push).toHaveBeenCalledWith("/scorecard");
    expect(localStorage.getItem(ONBOARDED_KEY)).toBe("u-9");
  });

  it("asks a signed-out visitor to sign in and come back", () => {
    search = "next=/vote";
    withAuth(fakeAuthStore("signed-out").store, <Onboarding />);
    expect(screen.getByRole("link", { name: "Sign in" }).getAttribute("href")).toBe("/login?next=%2Fvote");
  });

  it("offers a retry when the account didn't load", () => {
    const fake = fakeAuthStore("error");
    withAuth(fake.store, <Onboarding />);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(fake.store.retry).toHaveBeenCalled();
  });
});

describe("Google-only sign-in (#652)", () => {
  afterEach(() => vi.unstubAllEnvs());

  it("names no other provider on sign-in, onboarding or account settings", () => {
    vi.stubEnv("NEXT_PUBLIC_AUTH_PROVIDERS", "google.com");
    voteOnDevice(1);
    const screens = [
      [fakeAuthStore("signed-out"), <SignIn key="sign-in" />],
      [fakeAuthStore("signed-out"), <Onboarding key="onboarding-out" />],
      [fakeAuthStore("signed-in", { account: { id: "u-9", created_at: "2026-10-05T00:00:00Z" } }), <Onboarding key="onboarding" />],
      [fakeAuthStore("signed-in"), <AccountSettings key="settings" />],
    ] as const;
    for (const [fake, ui] of screens) {
      withAuth(fake.store, ui, () => fakeAccountBackend());
      expect(document.body.textContent).not.toMatch(/Apple|Microsoft/);
      cleanup();
    }
  });

  it("offers Google alone on sign-in", () => {
    vi.stubEnv("NEXT_PUBLIC_AUTH_PROVIDERS", "google.com");
    withAuth(fakeAuthStore("signed-out").store, <SignIn />);
    expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual(["Continue with Google"]);
  });
});

describe("Your data", () => {
  const exported: AccountExport = {
    user: fakeAccount,
    auth_uid: "firebase-uid",
    votes: [{ user_id: "u-1", bill_id: "hr-119-1", vote: "yea", voted_at: "2026-10-01T00:00:00Z" }],
    favorites: [],
  };

  function setup(deleteMe = vi.fn<(token: string) => Promise<void>>(async () => {})) {
    const fake = fakeAuthStore("signed-in");
    const api = { exportMe: vi.fn(async () => exported), deleteMe };
    const save = vi.fn();
    render(<YourData user={fake.store} api={api} save={save} />);
    return { fake, api, save };
  }

  it("says what the account keeps: votes, district and followed bills", () => {
    setup();
    expect(screen.getByText(/keeps your votes, your district and the bills you follow/)).toBeTruthy();
  });

  it("downloads exactly what GET /me/export returns", async () => {
    const { api, save } = setup();
    fireEvent.click(screen.getByRole("button", { name: "Download my data" }));
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(api.exportMe).toHaveBeenCalledWith("id-token-1");
    expect(JSON.parse(save.mock.calls[0][0] as string)).toEqual(exported);
    expect(save.mock.calls[0][1]).toBe("just-a-bill-account.json");
  });

  it("says so when the download fails", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const { api } = setup();
    api.exportMe.mockRejectedValueOnce(new Error("503"));
    fireEvent.click(screen.getByRole("button", { name: "Download my data" }));
    expect((await screen.findByRole("alert")).textContent).toMatch(/couldn't download/);
    vi.mocked(console.error).mockRestore();
  });

  it("deletes only after a confirmation, then signs out", async () => {
    const { api, fake } = setup();
    fireEvent.click(screen.getByRole("button", { name: "Delete my account" }));
    expect(api.deleteMe).not.toHaveBeenCalled();
    expect(screen.getByText(/deletes your votes, your district and the bills you follow/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByText(/can't be undone/)).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Delete my account" }));
    fireEvent.click(within(screen.getByRole("group")).getByRole("button", { name: "Delete my account" }));
    await waitFor(() => expect(fake.store.signOut).toHaveBeenCalled());
    expect(api.deleteMe).toHaveBeenCalledWith("id-token-1");
  });

  it("asks for a fresh sign-in when the API wants one", async () => {
    const recent = new ApiError(401, "Unauthorized", '{"code":"requires_recent_login"}');
    const { fake, api } = setup(vi.fn<(t: string) => Promise<void>>().mockRejectedValueOnce(recent).mockResolvedValueOnce());
    fireEvent.click(screen.getByRole("button", { name: "Delete my account" }));
    fireEvent.click(within(screen.getByRole("group")).getByRole("button", { name: "Delete my account" }));
    await waitFor(() => expect(fake.store.signOut).toHaveBeenCalled());
    expect(fake.store.reauthenticate).toHaveBeenCalled();
    expect(api.deleteMe).toHaveBeenCalledTimes(2);
  });

  it("keeps the account and says why when deleting fails, but not when the sign-in popup was closed", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const { fake } = setup(vi.fn(async () => Promise.reject(new ApiError(502, "Bad Gateway", "{}"))));
    fireEvent.click(screen.getByRole("button", { name: "Delete my account" }));
    fireEvent.click(within(screen.getByRole("group")).getByRole("button", { name: "Delete my account" }));
    expect((await screen.findByRole("alert")).textContent).toMatch(/removing your sign-in failed/);
    expect(fake.store.signOut).not.toHaveBeenCalled();
    cleanup();

    const recent = new ApiError(401, "Unauthorized", '{"code":"requires_recent_login"}');
    const closed = setup(vi.fn(async () => Promise.reject(recent)));
    closed.fake.store.reauthenticate.mockRejectedValueOnce({ code: "auth/popup-closed-by-user" });
    fireEvent.click(screen.getByRole("button", { name: "Delete my account" }));
    const group = screen.getByRole("group");
    fireEvent.click(within(group).getByRole("button", { name: "Delete my account" }));
    await waitFor(() => expect(closed.fake.store.reauthenticate).toHaveBeenCalled());
    await act(async () => {});
    expect(screen.queryByRole("alert")).toBeNull();
    expect((within(group).getByRole("button", { name: "Delete my account" }) as HTMLButtonElement).disabled).toBe(false);
    vi.mocked(console.error).mockRestore();
  });

  it("sits in settings next to the offer to add this device's votes", () => {
    voteOnDevice(1);
    withAuth(fakeAuthStore("signed-in").store, <AccountSettings />, () => fakeAccountBackend());
    expect(screen.getByRole("button", { name: "Download my data" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Add 1 vote to my account" })).toBeTruthy();
  });
});
