// @vitest-environment jsdom
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { cleanup, render, screen, within } from "@testing-library/react";
import Link from "next/link";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Navbar } from "@/components/layout/navbar";
import { Button, buttonClasses } from "@/components/ui/button";
import { axeViolations } from "@/test/axe";

vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  usePathname: () => "/",
}));

// The navbar reads the session in the browser (#137); a signed-out visitor sees "Sign in".
vi.mock("@/lib/auth/provider", () => ({ useUser: () => ({ status: "signed-out" }) }));

const { default: Home } = await import("@/app/page");

// #335: a <Button> inside a <Link> is a button inside a link, two nested interactive elements
// (axe nested-interactive). Links that look like buttons style the <Link> with buttonClasses().

afterEach(cleanup);

const SRC = path.resolve(__dirname, "../..");

function tsxFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) return entry.name === "__tests__" ? [] : tsxFiles(full);
    return entry.name.endsWith(".tsx") ? [full] : [];
  });
}

describe("links that look like buttons", () => {
  it("no <Link> in the app wraps a <Button>", () => {
    const nested: string[] = [];
    for (const file of tsxFiles(SRC)) {
      const source = readFileSync(file, "utf8");
      for (const match of source.matchAll(/<Link\b[\s\S]*?<\/Link>/g)) {
        if (/<Button\b/.test(match[0])) {
          const line = source.slice(0, match.index).split("\n").length;
          nested.push(`${path.relative(SRC, file)}:${line}`);
        }
      }
    }
    expect(nested).toEqual([]);
  });

  // #409: the last nestings were the navbar's and the home page's calls to action.
  async function renderStatic(page: () => Promise<React.ReactElement>) {
    vi.stubEnv("NEXT_PUBLIC_ACCOUNTS_ENABLED", "true");
    const container = document.createElement("div");
    container.innerHTML = renderToStaticMarkup(await page());
    vi.unstubAllEnvs();
    expect(container.querySelectorAll("a button")).toHaveLength(0);
    return container;
  }

  it("the navbar renders its call to action as a link, with no button inside a link", async () => {
    const container = await renderStatic(async () => <Navbar accounts />);
    // Sign in is optional, so it's the quiet variant on every page (#717).
    expect(within(container).getAllByRole("link", { name: "Sign in" })[0].className).toBe(
      buttonClasses({ variant: "ghost", size: "sm" })
    );
  });

  it("the home page renders its calls to action as links, with no button inside a link", async () => {
    const container = await renderStatic(async () => await Home());
    expect(within(container).getAllByRole("link", { name: "Sign in" })[0].className).toBe(
      buttonClasses({ variant: "ghost", size: "sm" })
    );
    expect(within(container).getAllByRole("link", { name: "Start voting" })[0].className).toBe(
      buttonClasses({ size: "sm" })
    );
  });

  it("buttonClasses gives a link the Button's styles", () => {
    render(
      <>
        <Button variant="outline" size="sm" className="w-full">
          As a button
        </Button>
        <Link href="/signup" className={buttonClasses({ variant: "outline", size: "sm", className: "w-full" })}>
          As a link
        </Link>
      </>
    );
    expect(screen.getByRole("link", { name: "As a link" }).className).toBe(
      screen.getByRole("button", { name: "As a button" }).className
    );
  });

  it("defaults to the primary, medium button", () => {
    expect(buttonClasses()).toContain("bg-primary text-primary-foreground");
    expect(buttonClasses()).toContain("h-10 px-4 text-sm");
  });

  it("a styled link has no axe violations", async () => {
    const { container } = render(
      <nav aria-label="Account">
        <Link href="/login" className={buttonClasses({ variant: "ghost", size: "sm" })}>
          Sign In
        </Link>
        <Link href="/signup" className={buttonClasses({ size: "sm" })}>
          Get Started
        </Link>
      </nav>
    );
    expect(await axeViolations(container)).toEqual([]);
  });
});
