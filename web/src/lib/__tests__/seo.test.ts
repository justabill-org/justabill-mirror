import { readdirSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import {
  MAX_SITEMAP_URLS,
  SITEMAP_PAGES,
  billJsonLd,
  billPreviewCopy,
  congressGovUrl,
  indexingAllowed,
  robotsRules,
  serializeJsonLd,
  siteOrigin,
  sitemapEntries,
} from "../seo";
import type { Bill } from "../types";

const bill: Bill = {
  id: "hr-119-1",
  congress: 119,
  bill_type: "hr",
  number: 1,
  title: "One Big Beautiful Bill Act",
  introduced_date: "2025-05-20T00:00:00Z",
  current_status: "became_law",
  policy_area: "Economics and Public Finance",
  updated_at: "2026-09-01T12:00:00Z",
};

describe("siteOrigin", () => {
  it("prefers NEXT_PUBLIC_SITE_URL, without a trailing slash or path", () => {
    expect(siteOrigin({ NEXT_PUBLIC_SITE_URL: "https://justabill.example/app/", VERCEL_URL: "x.vercel.app" })).toBe(
      "https://justabill.example"
    );
  });

  it("falls back to the production domain in production and the deployment URL elsewhere", () => {
    const vercel = { VERCEL_PROJECT_PRODUCTION_URL: "justabill.vercel.app", VERCEL_URL: "justabill-abc.vercel.app" };
    expect(siteOrigin({ ...vercel, VERCEL_ENV: "production" })).toBe("https://justabill.vercel.app");
    expect(siteOrigin({ ...vercel, VERCEL_ENV: "preview" })).toBe("https://justabill-abc.vercel.app");
  });

  it("falls back to localhost", () => {
    expect(siteOrigin({ NEXT_PUBLIC_SITE_URL: "not a url" })).toBe("http://localhost:3000");
    expect(siteOrigin({ PORT: "4000" })).toBe("http://localhost:4000");
  });
});

describe("robotsRules", () => {
  const production = { VERCEL_ENV: "production", NEXT_PUBLIC_SITE_URL: "https://justabill.example" };

  it("allows crawling in production, except personal pages and list variants, and points at the sitemap", () => {
    expect(indexingAllowed(production)).toBe(true);
    const robots = robotsRules(production);
    expect(robots.sitemap).toBe("https://justabill.example/sitemap.xml");
    expect(robots.rules).toEqual({
      userAgent: "*",
      allow: "/",
      disallow: ["/settings", "/login", "/signup", "/vote", "/scorecard", "/my-votes", "/bills?"],
    });
  });

  it("never blocks share pages, which link-preview crawlers must fetch", () => {
    const rules = robotsRules(production).rules;
    const disallow = [rules].flat().flatMap((r) => [r.disallow ?? []].flat());
    expect(disallow.some((path) => "/share/bill/hr-119-1/yea/image.png".startsWith(path))).toBe(false);
  });

  it.each([{}, { VERCEL_ENV: "preview" }, { VERCEL_ENV: "development" }])("disallows everything for %j", (env) => {
    expect(indexingAllowed(env)).toBe(false);
    expect(robotsRules(env)).toEqual({ rules: { userAgent: "*", disallow: "/" } });
  });
});

describe("sitemapEntries", () => {
  const origin = "https://justabill.example";
  const sharedPages = [
    { url: origin, changeFrequency: "daily" },
    { url: `${origin}/bills`, changeFrequency: "daily" },
    { url: `${origin}/about`, changeFrequency: "monthly" },
    { url: `${origin}/methodology`, changeFrequency: "monthly" },
    { url: `${origin}/privacy`, changeFrequency: "monthly" },
    { url: `${origin}/terms`, changeFrequency: "monthly" },
    { url: `${origin}/contact`, changeFrequency: "monthly" },
  ];

  it("lists the shared pages, including the trust pages, then each bill with its last update", () => {
    const entries = sitemapEntries(origin, [{ id: "hr-119-1", updated_at: "2026-09-01T12:00:00Z" }, { id: "s-119-2" }]);
    expect(entries).toEqual([
      ...sharedPages,
      { url: `${origin}/bills/hr-119-1`, lastModified: "2026-09-01T12:00:00Z" },
      { url: `${origin}/bills/s-119-2` },
    ]);
  });

  it("lists each member's page once, between the shared pages and the bills", () => {
    const entries = sitemapEntries(origin, [{ id: "hr-119-1" }], ["A000001", "B000002", "A000001"]);
    expect(entries.slice(sharedPages.length).map((e) => e.url)).toEqual([
      `${origin}/members/A000001`,
      `${origin}/members/B000002`,
      `${origin}/bills/hr-119-1`,
    ]);
  });

  it("stays within the 50,000-URL limit, cutting bills before members", () => {
    const bills = Array.from({ length: MAX_SITEMAP_URLS + 10 }, (_, i) => ({ id: `hr-119-${i + 1}` }));
    const entries = sitemapEntries(origin, bills, ["A000001"]);
    expect(entries).toHaveLength(MAX_SITEMAP_URLS);
    expect(entries.map((e) => e.url)).toContain(`${origin}/members/A000001`);
    expect(entries.at(-1)?.url).toBe(`${origin}/bills/hr-119-${MAX_SITEMAP_URLS - sharedPages.length - 1}`);
  });

  it("lists only pages the app has", () => {
    const appDir = path.resolve(__dirname, "../../app");
    const routes = readdirSync(appDir, { recursive: true, encoding: "utf8" })
      .filter((file) => path.basename(file) === "page.tsx")
      .map((file) => {
        const segments = path.dirname(file).split(path.sep).filter((s) => s !== "." && !/^\(.*\)$/.test(s));
        return `/${segments.join("/")}`;
      });
    expect(routes).toEqual(expect.arrayContaining(SITEMAP_PAGES));
  });

  it("never lists share pages", () => {
    const urls = sitemapEntries(origin, [{ id: "hr-119-1" }], ["A000001"]).map((e) => e.url);
    expect(urls.some((u) => u.includes("/share/"))).toBe(false);
  });
});

describe("billPreviewCopy", () => {
  it("prints the bill's number, congress, title and status", () => {
    const copy = billPreviewCopy(bill);
    expect(copy.eyebrow).toBe("H.R. 1 · 119th Congress");
    expect(copy.title).toBe("One Big Beautiful Bill Act");
    expect(copy.headline).toEqual([{ text: "Status: " }, { text: "Became law", strong: true }]);
    expect(copy.summary).toBe("H.R. 1: One Big Beautiful Bill Act");
    expect(copy.plain).toBe("H.R. 1 · 119th Congress. One Big Beautiful Bill Act. Status: Became law.");
  });

  it("shortens long titles and copes without a status", () => {
    const copy = billPreviewCopy({ ...bill, title: "word ".repeat(60), current_status: undefined });
    expect(copy.title?.length).toBeLessThanOrEqual(110);
    expect(copy.title?.endsWith("…")).toBe(true);
    expect(copy.headline).toEqual([{ text: "Read it and vote" }]);
  });
});

describe("congressGovUrl", () => {
  it.each([
    ["hr", 119, 1, "https://www.congress.gov/bill/119th-congress/house-bill/1"],
    ["s", 118, 22, "https://www.congress.gov/bill/118th-congress/senate-bill/22"],
    ["hjres", 119, 3, "https://www.congress.gov/bill/119th-congress/house-joint-resolution/3"],
    ["sconres", 111, 4, "https://www.congress.gov/bill/111th-congress/senate-concurrent-resolution/4"],
    ["hres", 112, 5, "https://www.congress.gov/bill/112th-congress/house-resolution/5"],
  ])("links %s %d-%d", (bill_type, congress, number, want) => {
    expect(congressGovUrl({ bill_type: bill_type as Bill["bill_type"], congress, number })).toBe(want);
  });

  it("has no link for an unknown type", () => {
    expect(congressGovUrl({ bill_type: "xyz" as Bill["bill_type"], congress: 119, number: 1 })).toBeUndefined();
  });
});

describe("billJsonLd", () => {
  it("describes the bill as schema.org Legislation", () => {
    const sponsor = { bioguideId: "A000001", name: "Ada Alvarez" };
    expect(billJsonLd(bill, "https://justabill.example", sponsor)).toEqual({
      "@context": "https://schema.org",
      "@type": "Legislation",
      name: "One Big Beautiful Bill Act",
      alternateName: "H.R. 1",
      legislationIdentifier: "H.R. 1 (119th Congress)",
      legislationType: "Bill",
      legislationJurisdiction: "US",
      inLanguage: "en-US",
      url: "https://justabill.example/bills/hr-119-1",
      sameAs: "https://www.congress.gov/bill/119th-congress/house-bill/1",
      legislationDate: "2025-05-20",
      dateModified: "2026-09-01T12:00:00Z",
      about: "Economics and Public Finance",
      legislationResponsible: {
        "@type": "Person",
        name: "Ada Alvarez",
        url: "https://justabill.example/members/A000001",
      },
    });
  });

  it.each([
    ["sjres", "Joint resolution"],
    ["hconres", "Concurrent resolution"],
    ["sres", "Simple resolution"],
    ["s", "Bill"],
  ])("calls a %s a %s", (billType, want) => {
    const data = billJsonLd({ ...bill, bill_type: billType as Bill["bill_type"] }, "https://x.example");
    expect(data.legislationType).toBe(want);
  });

  it("sets the abstract it's given", () => {
    expect(billJsonLd(bill, "https://x.example", undefined, "This bill does a thing.").abstract).toBe(
      "This bill does a thing."
    );
  });

  it("leaves out what the bill doesn't have", () => {
    const data = billJsonLd(
      { id: "hr-119-2", congress: 119, bill_type: "hr", number: 2, title: "Bare Act" },
      "https://x.example"
    );
    for (const key of ["legislationDate", "dateModified", "about", "legislationResponsible", "abstract"]) {
      expect(data).not.toHaveProperty(key);
    }
  });
});

describe("serializeJsonLd", () => {
  it("escapes < so a title can't close the script element", () => {
    const json = serializeJsonLd({ name: "</script><script>alert(1)</script>" });
    expect(json).not.toContain("<");
    expect(JSON.parse(json)).toEqual({ name: "</script><script>alert(1)</script>" });
  });
});
