// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { fakeAccount, fakeAuthStore } from "@/test/fake-auth";
import type { AuthStore } from "../auth/store";

// AuthProvider and useUser (#137), and the screens that read them in the browser: the navbar, the
// sign-in page and /settings. The store is faked; auth-store.test.ts covers the real one.

const replace = vi.fn();
let search = "";
vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  usePathname: () => "/bills",
  useRouter: () => ({ replace, push: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(search),
}));

const { AuthProvider, useUser } = await import("../auth/provider");
const { Navbar } = await import("@/components/layout/navbar");
const { SignIn } = await import("@/app/(auth)/login/sign-in");
const { AccountSettings } = await import("@/app/(app)/settings/account-settings");

const account = fakeAccount;
const fakeStore = fakeAuthStore;

function withStore(store: AuthStore, ui: React.ReactNode) {
  return render(<AuthProvider store={store}>{ui}</AuthProvider>);
}

afterEach(() => {
  cleanup();
  replace.mockClear();
  search = "";
});

describe("AuthProvider and useUser", () => {
  function Status() {
    const { status, account: a } = useUser();
    return <p>{`${status}:${a?.id ?? "none"}`}</p>;
  }

  it("starts the store on mount, follows its state and stops it on unmount", () => {
    const fake = fakeStore("loading");
    const { unmount } = withStore(fake.store, <Status />);
    expect(fake.store.start).toHaveBeenCalledTimes(1);
    expect(screen.getByText("loading:none")).toBeTruthy();

    fake.set({ status: "signed-in", account });
    expect(screen.getByText("signed-in:u-1")).toBeTruthy();

    unmount();
    expect(fake.stop).toHaveBeenCalled();
  });

  it("is disabled outside a provider, so nothing loads Firebase", () => {
    render(<Status />);
    expect(screen.getByText("disabled:none")).toBeTruthy();
  });
});

describe("Navbar", () => {
  const links = () => screen.queryAllByRole("link").map((a) => a.getAttribute("href"));

  it("shows neither account link while the session loads", () => {
    withStore(fakeStore("loading").store, <Navbar accounts />);
    expect(links()).not.toContain("/login");
    expect(links()).not.toContain("/settings");
  });

  it("links to sign-in when signed out and to settings when signed in", () => {
    const fake = fakeStore("signed-out");
    withStore(fake.store, <Navbar accounts />);
    expect(links()).toContain("/login");
    expect(links()).not.toContain("/signup");

    fake.set({ status: "signed-in", account });
    expect(links()).toContain("/settings");
    expect(links()).not.toContain("/login");
  });

  it("has no account links with accounts off, whatever the session", () => {
    withStore(fakeStore("signed-in").store, <Navbar />);
    expect(links()).not.toContain("/settings");
    expect(links()).not.toContain("/login");
  });
});

describe("SignIn", () => {
  it("offers Google, Apple and Microsoft, and signs in with the one clicked", () => {
    const fake = fakeStore("signed-out");
    withStore(fake.store, <SignIn />);
    const buttons = screen.getAllByRole("button").map((b) => b.textContent);
    expect(buttons).toEqual(["Continue with Google", "Continue with Apple", "Continue with Microsoft"]);

    fireEvent.click(screen.getByRole("button", { name: "Continue with Microsoft" }));
    expect(fake.store.signIn).toHaveBeenCalledWith("microsoft.com");
  });

  describe("with NEXT_PUBLIC_AUTH_PROVIDERS (#652)", () => {
    afterEach(() => vi.unstubAllEnvs());

    it("offers only the listed providers, in the listed order", () => {
      vi.stubEnv("NEXT_PUBLIC_AUTH_PROVIDERS", "microsoft.com,google.com");
      const fake = fakeStore("signed-out");
      withStore(fake.store, <SignIn />);
      const buttons = screen.getAllByRole("button").map((b) => b.textContent);
      expect(buttons).toEqual(["Continue with Microsoft", "Continue with Google"]);

      fireEvent.click(screen.getByRole("button", { name: "Continue with Google" }));
      expect(fake.store.signIn).toHaveBeenCalledWith("google.com");
    });

    it("says sign-in is unavailable when none is listed", () => {
      vi.stubEnv("NEXT_PUBLIC_AUTH_PROVIDERS", "");
      withStore(fakeStore("signed-out").store, <SignIn />);
      expect(screen.queryAllByRole("button")).toEqual([]);
      expect(screen.getByText(/Sign-in isn.t available right now/)).toBeTruthy();
    });
  });

  it("disables the buttons until the session is known", () => {
    withStore(fakeStore("loading").store, <SignIn />);
    for (const b of screen.getAllByRole("button")) expect((b as HTMLButtonElement).disabled).toBe(true);
  });

  it("goes back to a same-site ?next= once signed in", () => {
    search = "next=/settings";
    const fake = fakeStore("signed-out");
    withStore(fake.store, <SignIn />);
    expect(replace).not.toHaveBeenCalled();
    fake.set({ status: "signed-in", account });
    expect(replace).toHaveBeenCalledWith("/settings");
  });

  it("never follows ?next= off the site", () => {
    search = "next=https://evil.example";
    withStore(fakeStore("signed-in").store, <SignIn />);
    expect(replace).toHaveBeenCalledWith("/vote");
  });

  it("shows the sign-in error and the account error with a retry", () => {
    const fake = fakeStore("error", { signInError: "Too many sign-in attempts." });
    withStore(fake.store, <SignIn />);
    expect(screen.getByText("Too many sign-in attempts.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(fake.store.retry).toHaveBeenCalled();
  });

  it("says sign-in is unavailable when it's off in the build", () => {
    render(<SignIn />);
    expect(screen.getByText(/Sign-in isn.t available right now/)).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
  });
});

describe("AccountSettings", () => {
  it("asks a signed-out visitor to sign in, and comes back to /settings", () => {
    withStore(fakeStore("signed-out").store, <AccountSettings />);
    expect(screen.getByRole("link", { name: "Sign in" }).getAttribute("href")).toBe("/login?next=/settings");
  });

  it("shows the district and signs out", () => {
    const fake = fakeStore("signed-in");
    withStore(fake.store, <AccountSettings />);
    expect(screen.getByText(/Your district:/).textContent).toContain("CA-12");
    fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
    expect(fake.store.signOut).toHaveBeenCalled();
  });

  it("offers a retry when the account didn't load", () => {
    const fake = fakeStore("error");
    withStore(fake.store, <AccountSettings />);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(fake.store.retry).toHaveBeenCalled();
  });
});
