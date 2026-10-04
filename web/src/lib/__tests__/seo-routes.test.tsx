import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { Bill, BillDetailResponse, Member } from "../types";

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  getBill: vi.fn(),
  getBillIndex: vi.fn(),
  listCongresses: vi.fn(),
  listMembers: vi.fn(),
}));

const api = await import("../api");
const { ApiError } = api;
const { default: sitemap } = await import("@/app/sitemap");
const { default: robots } = await import("@/app/robots");
const siteImage = await import("@/app/opengraph-image");
const billImage = await import("@/app/(app)/bills/[id]/opengraph-image");
const billTwitterImage = await import("@/app/(app)/bills/[id]/twitter-image");
const { BillJsonLd } = await import("@/components/bill/bill-json-ld");

const bill: Bill = {
  id: "hr-119-1",
  congress: 119,
  bill_type: "hr",
  number: 1,
  title: "Example Act of 2025",
  current_status: "passed_house",
};

beforeEach(() => {
  vi.stubEnv("NEXT_PUBLIC_SITE_URL", "https://justabill.example");
  vi.mocked(api.getBill).mockReset().mockResolvedValue({ bill } as BillDetailResponse);
  vi.mocked(api.listCongresses).mockReset().mockResolvedValue([
    { number: 118, start_date: "2023-01-03", is_current: false },
    { number: 119, start_date: "2025-01-03", is_current: true },
  ]);
  vi.mocked(api.getBillIndex).mockReset().mockResolvedValue({
    congress: 119,
    bills: [{ id: "hr-119-1", updated_at: "2026-09-01T12:00:00Z" }, { id: "s-119-1" }],
  });
  mockMembers(["A000001", "B000002"]);
});

/** Serves the IDs as the API's member list would, in pages of at most 100. */
function mockMembers(ids: string[]) {
  vi.mocked(api.listMembers)
    .mockReset()
    .mockImplementation(async ({ offset = 0, limit = 20 } = {}) => {
      const page = Math.min(limit, 100);
      const items = ids.slice(offset, offset + page).map((id) => ({ bioguide_id: id }) as Member);
      return { items, total: ids.length, offset, limit: page };
    });
}

const SHARED_PAGES = [
  "https://justabill.example",
  "https://justabill.example/bills",
  "https://justabill.example/about",
  "https://justabill.example/methodology",
  "https://justabill.example/privacy",
  "https://justabill.example/terms",
  "https://justabill.example/contact",
];

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("sitemap", () => {
  it("lists the current congress's members and bills", async () => {
    const entries = await sitemap();
    expect(api.getBillIndex).toHaveBeenCalledWith(119);
    expect(api.listMembers).toHaveBeenCalledWith({ congress: 119, limit: 100 });
    expect(entries.map((e) => e.url)).toEqual([
      ...SHARED_PAGES,
      "https://justabill.example/members/A000001",
      "https://justabill.example/members/B000002",
      "https://justabill.example/bills/hr-119-1",
      "https://justabill.example/bills/s-119-1",
    ]);
  });

  it("pages through every member of the congress, 100 at a time", async () => {
    const ids = Array.from({ length: 541 }, (_, i) => `M${String(i).padStart(6, "0")}`);
    mockMembers(ids);
    const urls = (await sitemap()).map((e) => e.url).filter((u) => u.includes("/members/"));
    expect(urls).toHaveLength(541);
    expect(new Set(urls).size).toBe(541);
    expect(api.listMembers).toHaveBeenCalledTimes(6);
    expect(api.listMembers).toHaveBeenLastCalledWith({ congress: 119, limit: 100, offset: 500 });
  });

  it("makes one member request when the congress has no members yet", async () => {
    mockMembers([]);
    const entries = await sitemap();
    expect(entries.some((e) => e.url.includes("/members/"))).toBe(false);
    expect(api.listMembers).toHaveBeenCalledTimes(1);
  });

  it("uses the newest congress when none is marked current", async () => {
    vi.mocked(api.listCongresses).mockResolvedValue([
      { number: 119, start_date: "2025-01-03", is_current: false },
      { number: 118, start_date: "2023-01-03", is_current: false },
    ]);
    await sitemap();
    expect(api.getBillIndex).toHaveBeenCalledWith(119);
  });

  it("lists only the shared pages when there are no congresses", async () => {
    vi.mocked(api.listCongresses).mockResolvedValue([]);
    expect((await sitemap()).map((e) => e.url)).toEqual(SHARED_PAGES);
    expect(api.getBillIndex).not.toHaveBeenCalled();
    expect(api.listMembers).not.toHaveBeenCalled();
  });

  it("throws at request time when the API fails, so the last good sitemap stays", async () => {
    vi.mocked(api.getBillIndex).mockRejectedValue(new Error("API down"));
    await expect(sitemap()).rejects.toThrow("API down");
  });

  it("throws at request time when a member page fails, rather than serving a short sitemap", async () => {
    vi.mocked(api.listMembers).mockRejectedValue(new Error("API down"));
    await expect(sitemap()).rejects.toThrow("API down");
  });

  it("lists only the shared pages during the build, when the API can't be reached", async () => {
    vi.stubEnv("NEXT_PHASE", "phase-production-build");
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    vi.mocked(api.listCongresses).mockRejectedValue(new TypeError("fetch failed"));
    expect((await sitemap()).map((e) => e.url)).toEqual(SHARED_PAGES);
    expect(warn).toHaveBeenCalled();
    warn.mockRestore();
  });
});

describe("robots", () => {
  it("disallows everything outside production", () => {
    vi.stubEnv("VERCEL_ENV", "preview");
    expect(robots()).toEqual({ rules: { userAgent: "*", disallow: "/" } });
  });

  it("points production crawlers at the sitemap", () => {
    vi.stubEnv("VERCEL_ENV", "production");
    expect(robots().sitemap).toBe("https://justabill.example/sitemap.xml");
  });
});

const imageParams = (id: string) => ({ params: Promise.resolve({ id }) });

describe("bill preview image", () => {
  it("renders the bill's card as a PNG with an hour of CDN caching", async () => {
    const res = await billImage.default(imageParams("hr-119-1"));
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("image/png");
    expect(res.headers.get("cache-control")).toBe(
      "public, max-age=3600, s-maxage=3600, stale-while-revalidate=86400"
    );
    expect(api.getBill).toHaveBeenCalledWith("hr-119-1");
    expect(billImage.size).toEqual({ width: 1200, height: 630 });
    expect(billImage.contentType).toBe("image/png");
  });

  it("404s a malformed ID without calling the API", async () => {
    const res = await billImage.default(imageParams("HR-119-01"));
    expect(res.status).toBe(404);
    expect(api.getBill).not.toHaveBeenCalled();
  });

  it("404s a bill the API doesn't have", async () => {
    vi.mocked(api.getBill).mockRejectedValue(new ApiError(404, "Not Found", "{}"));
    expect((await billImage.default(imageParams("hr-119-99999"))).status).toBe(404);
  });

  it("fails (not a cached 404) when the API errors", async () => {
    vi.mocked(api.getBill).mockRejectedValue(new ApiError(500, "Internal Server Error", "{}"));
    await expect(billImage.default(imageParams("hr-119-1"))).rejects.toThrow("API error 500");
  });

  it("is also the X card", () => {
    expect(billTwitterImage.default).toBe(billImage.default);
    expect(billTwitterImage.alt).toBe(billImage.alt);
  });
});

describe("site preview image", () => {
  it("renders a PNG", async () => {
    const res = await siteImage.default();
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("image/png");
  });
});

describe("BillJsonLd", () => {
  it("renders Legislation JSON-LD with the site's URLs", () => {
    const html = renderToStaticMarkup(<BillJsonLd bill={bill} sponsor={{ bioguideId: "A000001", name: "Ada" }} />);
    const match = /^<script type="application\/ld\+json">(.*)<\/script>$/.exec(html);
    expect(match).not.toBeNull();
    const data = JSON.parse(match![1]);
    expect(data["@type"]).toBe("Legislation");
    expect(data.url).toBe("https://justabill.example/bills/hr-119-1");
    expect(data.legislationResponsible.url).toBe("https://justabill.example/members/A000001");
  });

  it("can't be broken out of by a bill title", () => {
    const html = renderToStaticMarkup(<BillJsonLd bill={{ ...bill, title: "</script><img src=x>" }} />);
    expect(html.match(/<\/script>/g)).toHaveLength(1);
    expect(html).not.toContain("<img");
  });
});
