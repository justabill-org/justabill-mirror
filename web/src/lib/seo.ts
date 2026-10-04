// Search and link-preview extras (#86): the sitemap, robots.txt, bill preview images and the
// bill page's JSON-LD. The share pages (#88) have their own metadata in share-metadata.ts.

import type { MetadataRoute } from "next";
import { ordinal } from "./graph";
import { plainText, truncateTitle, type CardCopy, type CardText } from "./share";
import { siteUrl } from "./site";
import type { SponsorEntry } from "./sponsors";
import { BILL_STATUS_LABELS, BILL_TYPE_LABELS, type Bill, type BillIndexEntry } from "./types";

type SeoEnv = Record<string, string | undefined>;

/** A sitemap file holds at most 50,000 URLs (sitemaps.org). */
export const MAX_SITEMAP_URLS = 50_000;

/**
 * Pages every visitor sees the same way, listed in the sitemap ahead of the members and bills: the
 * home page, the bill list and the trust pages (#75). There's no member index page to list.
 */
export const SITEMAP_PAGES = ["/", "/bills", "/about", "/methodology", "/privacy", "/terms", "/contact"];

/** Shared pages whose content follows the data; the trust pages change only with a release. */
const DAILY_PAGES = new Set(["/", "/bills"]);

/**
 * Personal, sign-in and voting pages: nothing in them is worth a search result, and crawling
 * them only costs renders. `/bills?` covers the list's search and filter variants, which would
 * each be a new ISR page; crawlers find the bills themselves through the sitemap. `/share/` is
 * deliberately absent: link-preview crawlers must fetch share pages and their images (#88).
 */
export const ROBOTS_DISALLOW = ["/settings", "/login", "/signup", "/vote", "/scorecard", "/my-votes", "/bills?"];

/**
 * The site's origin for absolute URLs in the sitemap, robots.txt and JSON-LD: NEXT_PUBLIC_SITE_URL
 * when it's set, else the Vercel URL Next.js itself falls back to for metadataBase, else localhost.
 */
export function siteOrigin(env: SeoEnv = process.env): string {
  const configured = siteUrl(env.NEXT_PUBLIC_SITE_URL);
  if (configured) return configured.origin;
  const vercelHost =
    env.VERCEL_ENV === "production" ? env.VERCEL_PROJECT_PRODUCTION_URL || env.VERCEL_URL : env.VERCEL_URL;
  if (vercelHost) return `https://${vercelHost}`;
  return `http://localhost:${env.PORT || 3000}`;
}

/** Only the production deployment is indexed; previews, local and CI builds tell crawlers to stay out. */
export function indexingAllowed(env: SeoEnv = process.env): boolean {
  return env.VERCEL_ENV === "production";
}

export function robotsRules(env: SeoEnv = process.env): MetadataRoute.Robots {
  if (!indexingAllowed(env)) {
    return { rules: { userAgent: "*", disallow: "/" } };
  }
  return {
    rules: { userAgent: "*", allow: "/", disallow: ROBOTS_DISALLOW },
    sitemap: `${siteOrigin(env)}/sitemap.xml`,
  };
}

/**
 * The sitemap: the shared pages, then each member's page (bioguide IDs), then every bill in the
 * index, capped at the sitemap limit. Members go before bills because a congress has a few hundred
 * of them and thousands of bills, so if the cap ever bites it cuts bills, not people.
 */
export function sitemapEntries(
  origin: string,
  bills: BillIndexEntry[],
  memberIds: string[] = []
): MetadataRoute.Sitemap {
  const pages: MetadataRoute.Sitemap = SITEMAP_PAGES.map((path) => ({
    url: `${origin}${path === "/" ? "" : path}`,
    changeFrequency: DAILY_PAGES.has(path) ? "daily" : "monthly",
  }));
  const memberPages: MetadataRoute.Sitemap = [...new Set(memberIds)].map((id) => ({
    url: `${origin}/members/${encodeURIComponent(id)}`,
  }));
  const billPages: MetadataRoute.Sitemap = bills.map((b) => ({
    url: `${origin}/bills/${encodeURIComponent(b.id)}`,
    ...(b.updated_at ? { lastModified: b.updated_at } : {}),
  }));
  return [...pages, ...memberPages, ...billPages].slice(0, MAX_SITEMAP_URLS);
}

/** Status changes a few times a day at most: an hour at the edge, a day of stale-while-revalidate. */
export const BILL_IMAGE_CACHE_CONTROL = "public, max-age=3600, s-maxage=3600, stale-while-revalidate=86400";

export const SITE_CARD_ALT =
  "Just a Bill: read bills in Congress, vote on them, and see how your representatives voted";

/** The site's default preview image (app/opengraph-image.tsx). */
export const SITE_CARD_COPY: CardCopy = {
  summary: "Just a Bill",
  eyebrow: "Congress in plain language",
  headline: [
    { text: "Read the bill. " },
    { text: "Cast your vote.", strong: true },
    { text: " See how your representatives voted." },
  ],
  note: "Nonpartisan. Bills and roll-call votes from the congressional record.",
  plain: SITE_CARD_ALT,
};

/** "H.R. 1" for a bill of type hr and number 1. */
function billLabel(bill: Pick<Bill, "bill_type" | "number">): string {
  return `${BILL_TYPE_LABELS[bill.bill_type] ?? bill.bill_type.toUpperCase()} ${bill.number}`;
}

/** The bill's preview image (its Open Graph card): number, congress, title and status. */
export function billPreviewCopy(bill: Bill): CardCopy {
  const label = billLabel(bill);
  const eyebrow = `${label} · ${ordinal(bill.congress)} Congress`;
  const title = truncateTitle(bill.title);
  const status = bill.current_status ? BILL_STATUS_LABELS[bill.current_status] : undefined;
  const headline: CardText[] = status
    ? [{ text: "Status: " }, { text: status, strong: true }]
    : [{ text: "Read it and vote" }];
  const note = "Read it in plain language, cast your own vote, and see how your representatives voted.";
  return {
    summary: `${label}: ${title}`,
    eyebrow,
    title,
    headline,
    note,
    plain: `${eyebrow}. ${title}. ${plainText(headline)}.`,
  };
}

/** Congress.gov's path segment for each bill type, as in /bill/119th-congress/house-bill/1. */
const CONGRESS_GOV_TYPES: Record<string, string> = {
  hr: "house-bill",
  s: "senate-bill",
  hjres: "house-joint-resolution",
  sjres: "senate-joint-resolution",
  hconres: "house-concurrent-resolution",
  sconres: "senate-concurrent-resolution",
  hres: "house-resolution",
  sres: "senate-resolution",
};

/** The bill's page on Congress.gov, the official record. */
export function congressGovUrl(bill: Pick<Bill, "congress" | "bill_type" | "number">): string | undefined {
  const type = CONGRESS_GOV_TYPES[bill.bill_type];
  return type ? `https://www.congress.gov/bill/${ordinal(bill.congress)}-congress/${type}/${bill.number}` : undefined;
}

/** schema.org's legislationType, in words. */
function legislationType(billType: string): string {
  if (billType.endsWith("jres")) return "Joint resolution";
  if (billType.endsWith("conres")) return "Concurrent resolution";
  if (billType.endsWith("res")) return "Simple resolution";
  return "Bill";
}

/**
 * schema.org Legislation for the bill page. Only facts from the congressional record go in: no
 * AI summary (search engines show structured data as fact) and no legal-force claim, which a
 * bill's status alone can't settle.
 */
export function billJsonLd(
  bill: Bill,
  origin: string,
  sponsor?: Pick<SponsorEntry, "bioguideId" | "name">,
  abstract?: string,
): Record<string, unknown> {
  const label = billLabel(bill);
  const data: Record<string, unknown> = {
    "@context": "https://schema.org",
    "@type": "Legislation",
    name: bill.title,
    alternateName: label,
    legislationIdentifier: `${label} (${ordinal(bill.congress)} Congress)`,
    legislationType: legislationType(bill.bill_type),
    legislationJurisdiction: "US",
    inLanguage: "en-US",
    url: `${origin}/bills/${encodeURIComponent(bill.id)}`,
  };
  const official = congressGovUrl(bill);
  if (official) data.sameAs = official;
  if (bill.introduced_date) data.legislationDate = bill.introduced_date.slice(0, 10);
  if (bill.updated_at) data.dateModified = bill.updated_at;
  if (bill.policy_area) data.about = bill.policy_area;
  // The CRS summary's first paragraph, never the AI summary (docs/design/197-crs-summaries.md).
  if (abstract) data.abstract = abstract;
  if (sponsor) {
    data.legislationResponsible = {
      "@type": "Person",
      name: sponsor.name,
      url: `${origin}/members/${encodeURIComponent(sponsor.bioguideId)}`,
    };
  }
  return data;
}

/**
 * JSON for an inline <script type="application/ld+json">. `<` is escaped so a title containing
 * "</script>" can't end the script element early (Next.js docs, "JSON-LD").
 */
export function serializeJsonLd(data: unknown): string {
  return JSON.stringify(data).replace(/</g, "\\u003c");
}
