// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type { AnchorHTMLAttributes, ReactNode } from "react";
import { CongressSwitch, switchChoices, useCongressSelection } from "../congress/congress-switch";
import { axeViolations } from "@/test/axe";

let pathname = "/scorecard";
let search = "";
vi.mock("next/navigation", () => ({
  usePathname: () => pathname,
  useSearchParams: () => new URLSearchParams(search),
}));
// The real Link drops `scroll`; this one shows it, so the tests can check the page doesn't jump.
vi.mock("next/link", () => ({
  default: ({
    href,
    scroll,
    children,
    ...rest
  }: AnchorHTMLAttributes<HTMLAnchorElement> & { href: string; scroll?: boolean; children: ReactNode }) => (
    <a href={href} data-scroll={String(scroll)} {...rest}>
      {children}
    </a>
  ),
}));

afterEach(() => {
  cleanup();
  pathname = "/scorecard";
  search = "";
});

const offered = [119, 118];
const counts = { 119: 25, 118: 12 };

function links() {
  return screen.getAllByRole("link").map((a) => ({
    name: a.getAttribute("aria-label"),
    text: a.textContent,
    href: a.getAttribute("href"),
    current: a.getAttribute("aria-current") === "true",
  }));
}

/** The switch for the selection the page reads from the URL, as LocalScorecard renders it. */
function Switch({ counts, offered }: { counts: Record<number, number>; offered: readonly number[] }) {
  return <CongressSwitch counts={counts} offered={offered} selected={useCongressSelection(offered)} />;
}

describe("CongressSwitch", () => {
  it("offers All first, then each congress newest first, each with the visitor's vote count", () => {
    render(<Switch counts={counts} offered={offered} />);
    expect(screen.getByRole("navigation", { name: "Compare your votes on bills from" })).toBeTruthy();
    expect(links()).toEqual([
      { name: "All, 37 votes", text: "All37 votes", href: "/scorecard", current: true },
      {
        name: "2025–26 (119th), 25 votes",
        text: "2025–2625 votes",
        href: "/scorecard?congress=119",
        current: false,
      },
      {
        name: "2023–24 (118th), 12 votes",
        text: "2023–2412 votes",
        href: "/scorecard?congress=118",
        current: false,
      },
    ]);
  });

  it("says vote, not votes, for one", () => {
    render(<Switch counts={{ 119: 1, 118: 1 }} offered={offered} />);
    expect(links().map((l) => l.name)).toEqual(["All, 2 votes", "2025–26 (119th), 1 vote", "2023–24 (118th), 1 vote"]);
  });

  it("marks the congress in the URL, keeps the other parameters and doesn't scroll", () => {
    search = "congress=118&ref=share";
    render(<Switch counts={counts} offered={offered} />);
    expect(links().map((l) => [l.href, l.current])).toEqual([
      ["/scorecard?ref=share", false],
      ["/scorecard?ref=share&congress=119", false],
      ["/scorecard?ref=share&congress=118", true],
    ]);
    for (const a of screen.getAllByRole("link")) expect(a.dataset.scroll).toBe("false");
  });

  it.each([
    ["all", "congress=all"],
    ["one it doesn't offer", "congress=7"],
    ["junk", "congress=abc"],
    ["every congress", "congress=119&congress=118"],
  ])("compares everything, with All marked, for ?congress= naming %s", (_, query) => {
    search = query;
    render(<Switch counts={counts} offered={offered} />);
    expect(links().filter((l) => l.current).map((l) => l.text)).toEqual(["All37 votes"]);
  });

  it("marks nothing when the page compares a congress it doesn't offer", () => {
    render(<CongressSwitch counts={counts} offered={[119, 118, 117]} selected={[117]} />);
    expect(links().filter((l) => l.current)).toEqual([]);
  });

  it("calls the choice All, not Both, with three congresses, and lets the fourth segment wrap", () => {
    render(<Switch counts={{ 120: 3, 119: 2, 118: 1 }} offered={[120, 119, 118]} />);
    expect(links().map((l) => l.text)).toEqual(["All6 votes", "2027–283 votes", "2025–262 votes", "2023–241 vote"]);
    // Three columns on a phone (the fourth goes to a second row), one row from sm up.
    const nav = screen.getByRole("navigation");
    expect(nav.className).toContain("grid-cols-3");
    expect(nav.className).toContain("sm:grid-flow-col");
  });

  it("doesn't render with counted votes in only one congress, even with roll calls in two", () => {
    const { container, rerender } = render(<Switch counts={{ 119: 25 }} offered={offered} />);
    expect(container.innerHTML).toBe("");
    rerender(<Switch counts={{}} offered={offered} />);
    expect(container.innerHTML).toBe("");
    // A congress whose votes were all skips has no counted votes, so it isn't offered either.
    rerender(<Switch counts={{ 119: 25, 118: 0 }} offered={offered} />);
    expect(container.innerHTML).toBe("");
  });

  it("doesn't offer a congress the visitor voted in but that has no roll calls loaded", () => {
    const { container } = render(<Switch counts={counts} offered={[119]} />);
    expect(container.innerHTML).toBe("");
  });

  it("has no axe violations", async () => {
    search = "congress=118";
    const { container } = render(<Switch counts={counts} offered={offered} />);
    expect(await axeViolations(container)).toEqual([]);
  });
});

describe("switchChoices", () => {
  it("keeps the offered congresses with counted votes, in the offered order", () => {
    expect(switchChoices({ 118: 2, 119: 4, 117: 1 }, [119, 118])).toEqual([119, 118]);
    expect(switchChoices({ 118: 2 }, [119, 118])).toEqual([118]);
    expect(switchChoices({}, [])).toEqual([]);
  });
});

describe("useCongressSelection", () => {
  function Selection({ offered }: { offered: readonly number[] }) {
    return <output>{useCongressSelection(offered).join(",") || "all"}</output>;
  }

  it("round-trips the URL: a link's congress is what the page then selects", () => {
    render(<Switch counts={counts} offered={offered} />);
    const href = links()[2].href ?? "";
    cleanup();

    search = href.split("?")[1] ?? "";
    render(<Selection offered={offered} />);
    expect(screen.getByRole("status").textContent).toBe("118");
  });

  it("selects every congress without a filter, with one naming them all, and for ?congress=all", () => {
    for (const query of ["", "congress=118&congress=119", "congress=all"]) {
      search = query;
      render(<Selection offered={offered} />);
      expect(screen.getByRole("status").textContent).toBe("all");
      cleanup();
    }
  });
});
