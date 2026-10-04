// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Logo } from "@/components/layout/logo";
import { Navbar } from "@/components/layout/navbar";
import { SharePage } from "@/components/share/share-page";
import { axeViolations } from "@/test/axe";

// #661: one header and one logo on every page. Home keeps a call to action; phones get the menu.

let pathname: string | null = "/bills";
vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  usePathname: () => pathname,
}));
vi.mock("@/lib/auth/provider", () => ({ useUser: () => ({ status: "signed-out" }) }));

const { default: Home } = await import("@/app/page");
const { default: AppLayout } = await import("@/app/(app)/layout");
const { default: TrustLayout } = await import("@/app/(public)/(trust)/layout");
const { default: AuthLayout } = await import("@/app/(auth)/layout");
const { default: RootNotFound } = await import("@/app/not-found");
const { default: RootError } = await import("@/app/error");
const { default: GlobalError } = await import("@/app/global-error");

const NAV_LINKS = ["Bills", "Vote", "Scorecard", "My votes"];

beforeEach(() => {
  pathname = "/bills";
  vi.stubEnv("NEXT_PUBLIC_ACCOUNTS_ENABLED", "true");
});
afterEach(cleanup);

function staticContainer(element: React.ReactElement): HTMLElement {
  const container = document.createElement("div");
  container.innerHTML = renderToStaticMarkup(element);
  return container;
}

function shareData() {
  return {
    copy: {
      summary: "A sample card",
      eyebrow: "H.R. 0000 · 119th Congress",
      headline: [{ text: "Sample headline" }],
      note: "Sample note.",
      plain: "Sample headline",
    },
    actionHref: "/vote",
    actionLabel: "Vote on it",
  };
}

const error = Object.assign(new Error("boom"), { digest: "123" });

// Every surface that draws its own page chrome: the layouts and the pages outside a layout.
const PAGES: [string, () => Promise<React.ReactElement>][] = [
  ["the home page", async () => await Home()],
  ["the app pages", async () => AppLayout({ children: createElement("p", null, "page") })],
  ["the trust pages", async () => TrustLayout({ children: createElement("p", null, "page") })],
  ["the sign-in pages", async () => AuthLayout({ children: createElement("p", null, "page") })],
  ["the share pages", async () => <SharePage data={shareData()} />],
  ["the 404 page", async () => <RootNotFound />],
  ["the error page", async () => <RootError error={error} retry={() => {}} />],
];

describe("one header on every page", () => {
  it.each(PAGES)("%s render the shared header, logo and links", async (_, page) => {
    const container = staticContainer(await page());
    const navs = container.querySelectorAll('header nav[aria-label="Main"]');
    expect(navs).toHaveLength(1);
    const nav = navs[0] as HTMLElement;
    expect(within(nav).getByRole("link", { name: "Just a Bill" }).getAttribute("href")).toBe("/");
    expect(nav.querySelector('img[src*="bill-icon.jpg"]')).not.toBeNull();
    for (const name of NAV_LINKS) expect(within(nav).getAllByRole("link", { name }).length).toBeGreaterThan(0);
    expect(within(nav).getByRole("link", { name: "Sign in" }).getAttribute("href")).toBe("/login");
    // The old "JB" text tile is gone: the mascot is the one mark.
    expect(container.textContent).not.toMatch(/\bJB\b/);
  });

  it("the footer uses the same logo", async () => {
    const container = staticContainer(AppLayout({ children: null }));
    const footer = container.querySelector("footer") as HTMLElement;
    expect(within(footer).getByRole("link", { name: "Just a Bill" }).getAttribute("href")).toBe("/");
    expect(footer.querySelector('img[src*="bill-icon.jpg"]')).not.toBeNull();
  });

  it("the root crash page shows the logo, without the navbar that may have failed", () => {
    const container = staticContainer(<GlobalError error={error} retry={() => {}} />);
    const header = container.querySelector("header") as HTMLElement;
    expect(within(header).getByRole("link", { name: "Just a Bill" }).getAttribute("href")).toBe("/");
    expect(header.querySelector('img[src*="bill-icon.jpg"]')).not.toBeNull();
    expect(container.textContent).not.toMatch(/\bJB\b/);
  });

  it("only the home page has the Start voting call to action", async () => {
    const home = staticContainer(await Home());
    const homeNav = home.querySelector('nav[aria-label="Main"]') as HTMLElement;
    expect(within(homeNav).getByRole("link", { name: "Start voting" }).getAttribute("href")).toBe("/vote");

    for (const [, page] of PAGES.slice(1)) {
      const nav = staticContainer(await page()).querySelector('nav[aria-label="Main"]') as HTMLElement;
      expect(within(nav).queryByRole("link", { name: "Start voting" })).toBeNull();
    }
  });
});

describe("the logo", () => {
  it("is one link home named by its text, with a decorative image", () => {
    render(<Logo />);
    const link = screen.getByRole("link", { name: "Just a Bill" });
    expect(link.getAttribute("href")).toBe("/");
    expect(link.querySelector("img")?.getAttribute("alt")).toBe("");
  });
});

describe("the header on a phone", () => {
  const menuButton = () => screen.getByRole("button", { name: "Toggle navigation menu" });

  it("hides the desktop links below md and shows the menu button instead", () => {
    render(<Navbar accounts cta={{ href: "/vote", label: "Start voting" }} />);
    expect(menuButton().className).toContain("md:hidden");
    const desktop = [...screen.getByRole("navigation", { name: "Main" }).querySelectorAll(".md\\:flex")];
    expect(desktop).toHaveLength(2);
    for (const group of desktop) expect(group.className).toMatch(/(^| )hidden( |$)/);
    expect(within(desktop[0] as HTMLElement).getByRole("link", { name: "Bills" })).toBeTruthy();
    expect(within(desktop[1] as HTMLElement).getByRole("link", { name: "Start voting" })).toBeTruthy();
  });

  it("opens a menu with the links, Sign in and the home page's call to action", async () => {
    pathname = "/";
    const { container } = render(<Navbar accounts cta={{ href: "/vote", label: "Start voting" }} />);
    expect(menuButton().getAttribute("aria-expanded")).toBe("false");
    expect(await axeViolations(container)).toEqual([]);

    fireEvent.click(menuButton());
    expect(menuButton().getAttribute("aria-expanded")).toBe("true");
    const menu = container.querySelector(".md\\:hidden.border-t") as HTMLElement;
    const names = within(menu)
      .getAllByRole("link")
      .map((link) => link.textContent);
    expect(names).toEqual([...NAV_LINKS, "Sign in", "Start voting"]);
    expect(await axeViolations(container)).toEqual([]);

    // jsdom can't navigate, so keep the click from trying; the menu still closes.
    container.addEventListener("click", (event) => event.preventDefault());
    fireEvent.click(within(menu).getByRole("link", { name: "Start voting" }));
    expect(menuButton().getAttribute("aria-expanded")).toBe("false");
  });

  it("has no call to action in the menu on other pages", () => {
    const { container } = render(<Navbar accounts />);
    fireEvent.click(menuButton());
    const menu = container.querySelector(".md\\:hidden.border-t") as HTMLElement;
    expect(within(menu).queryByRole("link", { name: "Start voting" })).toBeNull();
    expect(within(menu).getByRole("link", { name: "Bills" }).getAttribute("aria-current")).toBe("page");
  });
});
