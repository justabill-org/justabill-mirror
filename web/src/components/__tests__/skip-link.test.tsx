// @vitest-environment jsdom
import { cleanup, render } from "@testing-library/react";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SharePage } from "@/components/share/share-page";
import { axeViolations } from "@/test/axe";

// #955: "Skip to main content" is the first thing Tab reaches on every page, and it points at that
// page's one <main>, which can take focus. The browser half (Tab, Enter, focus lands in <main>, in
// light and dark) is in e2e/a11y.spec.ts.

vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  usePathname: () => "/bills",
}));
vi.mock("@/lib/auth/provider", () => ({ useUser: () => ({ status: "signed-out" }) }));

const { default: Home } = await import("@/app/page");
const { default: AppLayout } = await import("@/app/(app)/layout");
const { default: TrustLayout } = await import("@/app/(public)/(trust)/layout");
const { default: AuthLayout } = await import("@/app/(auth)/layout");
const { default: RootNotFound } = await import("@/app/not-found");
const { default: RootError } = await import("@/app/error");

beforeEach(() => vi.stubEnv("NEXT_PUBLIC_ACCOUNTS_ENABLED", "true"));
afterEach(cleanup);

const error = Object.assign(new Error("boom"), { digest: "123" });
const page = () => createElement("p", null, "page");
const shareData = {
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

// Every surface that renders its own <main>: the layouts, and the pages outside a layout. The home
// page is first: it renders its own header and <main> (Review Focus 1 of #951).
const PAGES: [string, () => Promise<React.ReactElement>][] = [
  ["the home page", async () => await Home()],
  ["the app pages", async () => AppLayout({ children: page() })],
  ["the trust pages", async () => TrustLayout({ children: page() })],
  ["the sign-in pages", async () => AuthLayout({ children: page() })],
  ["the share pages", async () => <SharePage data={shareData} />],
  ["the 404 page", async () => <RootNotFound />],
  ["the error page", async () => <RootError error={error} retry={() => {}} />],
];

const FOCUSABLE = 'a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])';

function staticContainer(element: React.ReactElement): HTMLElement {
  const container = document.createElement("div");
  container.innerHTML = renderToStaticMarkup(element);
  return container;
}

describe("the skip link", () => {
  it.each(PAGES)("is the first stop on %s and points at its one focusable <main>", async (_, render) => {
    const container = staticContainer(await render());
    const first = container.querySelector(FOCUSABLE) as HTMLAnchorElement;
    expect(first.textContent).toBe("Skip to main content");
    expect(first.getAttribute("href")).toBe("#main");
    expect(first.closest("header")).not.toBeNull();

    const mains = container.querySelectorAll("main");
    expect(mains).toHaveLength(1);
    expect(mains[0].id).toBe("main");
    // -1: following the link focuses <main> itself, without adding it to the Tab order.
    expect(mains[0].getAttribute("tabindex")).toBe("-1");
  });

  it("passes axe on a page", async () => {
    const { container } = render(AppLayout({ children: page() }));
    expect(await axeViolations(container)).toEqual([]);
  });
});
