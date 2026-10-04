import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { accountsEnabled } from "../accounts";

const cookieReads = vi.fn();

vi.mock("next/headers", () => ({
  cookies: async () => {
    cookieReads();
    return { get: (name: string) => (name === "dev-user-id" ? { value: "dev-1" } : undefined) };
  },
}));

vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  usePathname: () => "/bills",
}));

const { default: AppLayout } = await import("@/app/(app)/layout");
const { default: AuthLayout } = await import("@/app/(auth)/layout");
const { default: SettingsPage } = await import("@/app/(app)/settings/page");
const { default: Home } = await import("@/app/page");
const { AuthProvider } = await import("../auth/provider");

// notFound() throws an error whose digest names the 404 fallback.
function isNotFound(err: unknown): boolean {
  return typeof err === "object" && err !== null && String((err as { digest?: unknown }).digest).includes("404");
}

async function rejectsNotFound(fn: () => unknown) {
  let caught: unknown;
  try {
    await fn();
  } catch (err) {
    caught = err;
  }
  expect(isNotFound(caught)).toBe(true);
}

const ACCOUNT_LINKS = ['href="/login"', 'href="/signup"', 'href="/settings"'];

describe("accountsEnabled", () => {
  it.each([
    ["true", "production", true],
    ["1", "production", true],
    [" TRUE ", "production", true],
    ["false", "development", false],
    ["0", "development", false],
    [undefined, "development", true],
    [undefined, "test", true],
    [undefined, "production", false],
    ["", "production", false],
    ["yes", "production", false],
  ])("flag %j with NODE_ENV %s is %s", (flag, nodeEnv, want) => {
    expect(accountsEnabled(flag, nodeEnv)).toBe(want);
  });
});

describe("with accounts off", () => {
  beforeEach(() => {
    vi.stubEnv("NEXT_PUBLIC_ACCOUNTS_ENABLED", "false");
    cookieReads.mockClear();
  });
  afterEach(() => vi.unstubAllEnvs());

  it("renders not-found for /login and /signup", async () => {
    await rejectsNotFound(() => AuthLayout({ children: "form" }));
  });

  it("renders not-found for /settings without reading the cookie", async () => {
    await rejectsNotFound(() => SettingsPage());
    expect(cookieReads).not.toHaveBeenCalled();
  });

  it("has no account links in the navbar and doesn't read the cookie", async () => {
    const html = renderToStaticMarkup(await AppLayout({ children: createElement("p", null, "page") }));
    expect(html).toContain('href="/vote"');
    for (const link of ACCOUNT_LINKS) expect(html).not.toContain(link);
    expect(html).not.toContain("Sign In");
    expect(cookieReads).not.toHaveBeenCalled();
  });

  it("sends the home page's calls to action to /vote", async () => {
    const html = renderToStaticMarkup(await Home());
    for (const link of ACCOUNT_LINKS) expect(html).not.toContain(link);
    expect(html).toContain('href="/vote"');
  });
});

describe("with accounts on", () => {
  beforeEach(() => {
    vi.stubEnv("NEXT_PUBLIC_ACCOUNTS_ENABLED", "true");
    cookieReads.mockClear();
  });
  afterEach(() => vi.unstubAllEnvs());

  it("renders the sign-in pages", () => {
    const html = renderToStaticMarkup(AuthLayout({ children: createElement("p", null, "the form") }));
    expect(html).toContain("the form");
  });

  // The layout stays cacheable (#74): the server never reads a cookie. Outside an AuthProvider
  // (and so with no Firebase session to wait for), the navbar links to sign-in; with one, the
  // browser fills in the link once the session loads (auth-provider.test.tsx).
  it("shows the sign-in link in the navbar without reading a cookie", async () => {
    const html = renderToStaticMarkup(await AppLayout({ children: "page" }));
    expect(cookieReads).not.toHaveBeenCalled();
    expect(html).toContain('href="/login"');
    expect(html).not.toContain('href="/signup"');
  });

  // With sign-in configured the server knows no session: it renders neither account link, and the
  // browser fills one in, so the same cached HTML serves everyone (#74).
  it("renders the same navbar for everyone when sign-in is configured", async () => {
    vi.stubEnv("NEXT_PUBLIC_FIREBASE_API_KEY", "fake-api-key");
    vi.stubEnv("NEXT_PUBLIC_FIREBASE_AUTH_DOMAIN", "localhost");
    vi.stubEnv("NEXT_PUBLIC_FIREBASE_PROJECT_ID", "demo-justabill");
    const layout = await AppLayout({ children: "page" });
    const html = renderToStaticMarkup(createElement(AuthProvider, null, layout));
    expect(cookieReads).not.toHaveBeenCalled();
    for (const link of ACCOUNT_LINKS) expect(html).not.toContain(link);
    expect(html).toContain('href="/vote"');
  });

  it("renders /settings without reading a cookie", async () => {
    const html = renderToStaticMarkup(await SettingsPage());
    expect(cookieReads).not.toHaveBeenCalled();
    expect(html).toContain("Settings");
  });

  it("links to sign-in from the home page", async () => {
    const html = renderToStaticMarkup(await Home());
    expect(html).toContain('href="/login"');
  });
});
