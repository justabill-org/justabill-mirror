import { afterEach, describe, it, expect, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { Pagination } from "../ui/pagination";

const nav = vi.hoisted(() => ({ query: "" }));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn() }),
  useSearchParams: () => new URLSearchParams(nav.query),
}));

/** The page numbers the nav offers, in order. */
function pageButtons(html: string): number[] {
  return [...html.matchAll(/<button[^>]*class="[^"]*min-w-9[^"]*"[^>]*>(\d+)<\/button>/g)].map((m) =>
    Number(m[1])
  );
}

describe("Pagination", () => {
  afterEach(() => {
    nav.query = "";
  });

  it("renders nothing for a single page", () => {
    expect(renderToStaticMarkup(<Pagination total={10} offset={0} limit={12} />)).toBe("");
  });

  it("links every page of a short list without a cap note", () => {
    const html = renderToStaticMarkup(<Pagination total={50} offset={0} limit={12} />);
    expect(pageButtons(html)).toEqual([1, 2, 3, 4, 5]);
    expect(html).not.toContain("Narrow the list");
  });

  it("stops at a list's own cap when it's below the API's (#666)", () => {
    const html = renderToStaticMarkup(<Pagination total={2_737} offset={0} limit={12} maxOffset={88} />);
    expect(pageButtons(html)).toEqual([1, 2, 3, 4, 5, 8]);
    expect(html).toContain("Showing the first 8 pages. Narrow the list with filters to see more.");
  });

  it("doesn't link pages past the API's offset cap on a long list", () => {
    const html = renderToStaticMarkup(<Pagination total={50_000} offset={0} limit={12} />);
    expect(pageButtons(html)).toEqual([1, 2, 3, 4, 5, 834]);
    expect(html).not.toContain(">4167<");
    expect(html).toContain("Showing the first 834 pages. Narrow the list with filters to see more.");
  });

  it("disables Next on the last reachable page", () => {
    const html = renderToStaticMarkup(<Pagination total={50_000} offset={9996} limit={12} />);
    expect(pageButtons(html)).toEqual([1, 830, 831, 832, 833, 834]);
    expect(html).toMatch(/<button[^>]*disabled=""[^>]*aria-label="Next page"/);
    expect(html).toContain('aria-current="page"');
  });

  it("stops a search's pages at the API's search offset cap (#619)", () => {
    nav.query = "q=water";
    const html = renderToStaticMarkup(<Pagination total={5000} offset={0} limit={12} />);
    expect(pageButtons(html)).toEqual([1, 2, 3, 4, 5, 42]);
    expect(html).toContain("Showing the first 42 pages. Narrow the search to see more.");
  });

  it("treats a search of only spaces as no search", () => {
    nav.query = "q=%20%20";
    const html = renderToStaticMarkup(<Pagination total={5000} offset={0} limit={12} />);
    expect(pageButtons(html)).toEqual([1, 2, 3, 4, 5, 417]);
  });

  // #688: Previous, five numbers, two ellipses and Next made the page 344px wide at 320px. Below sm
  // the nav shows Previous, "Page N of M" and Next; the numbered row comes back from sm up.
  describe("on a phone", () => {
    const html = renderToStaticMarkup(<Pagination total={50_000} offset={120} limit={12} />);

    it("says which page this is, marked current, only below sm", () => {
      expect(html).toMatch(/<span aria-current="page" class="[^"]*\bsm:hidden\b[^"]*">Page 11 of 834<\/span>/);
    });

    it("shows the numbered row only from sm up, its current page still marked", () => {
      const row = html.match(/<span class="hidden items-center gap-1 sm:flex">(.*)<\/span><button/)?.[1] ?? "";
      expect(pageButtons(row)).toEqual([1, 9, 10, 11, 12, 13, 834]);
      expect(row).toMatch(/<button[^>]*aria-current="page"[^>]*>11<\/button>/);
    });

    it("keeps Previous and Next on every screen, at least 24px square", () => {
      for (const name of ["Previous page", "Next page"]) {
        const cls = html.match(new RegExp(`<button class="([^"]*)"[^>]*aria-label="${name}"`))?.[1] ?? "";
        expect(cls.split(" ")).toContain("h-8"); // 32px tall
        expect(cls.split(" ")).toContain("px-3"); // 12px either side of a 16px icon: 40px wide
        expect(cls).not.toMatch(/(^|\s)hidden(\s|$)/);
      }
    });

    it("formats large page numbers", () => {
      const big = renderToStaticMarkup(<Pagination total={50_000} offset={0} limit={1} />);
      expect(big).toContain(">Page 1 of 10,001</span>");
    });
  });
});
