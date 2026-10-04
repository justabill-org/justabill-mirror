import type { MetadataRoute } from "next";
import { getBillIndex, listCongresses, listMembers } from "@/lib/api";
import { siteOrigin, sitemapEntries } from "@/lib/seo";
import type { BillIndexEntry } from "@/lib/types";

// The sitemap (#86, #338): the home page, the bill list, the trust pages, and every member and
// bill page in the current congress. /share/* pages are left out on purpose: they're noindex
// personal statements (#88, wiki "Share Cards"). robots.ts must still let crawlers fetch them.

// Regenerated at most hourly. The build can't reach the API, so the built sitemap lists only the
// shared pages and the members and bills fill in on the first regeneration. After that, a failed
// regeneration throws, and Next.js keeps serving the last good sitemap rather than a short one.
export const revalidate = 3600;

const BUILD_PHASE = "phase-production-build";

/** The API's largest page (`limit` is clamped to 100), so a congress's ~540 members take 6 requests. */
const MEMBER_PAGE_SIZE = 100;

async function currentCongress(): Promise<number | undefined> {
  const congresses = await listCongresses();
  const current = congresses.find((c) => c.is_current) ?? congresses.toSorted((a, b) => b.number - a.number)[0];
  return current?.number;
}

/** Every member who served in the congress: the first page gives the total, the rest load in parallel. */
async function congressMemberIds(congress: number): Promise<string[]> {
  const first = await listMembers({ congress, limit: MEMBER_PAGE_SIZE });
  if (first.items.length === 0) return [];
  const step = first.items.length;
  const offsets: number[] = [];
  for (let offset = step; offset < first.total; offset += step) offsets.push(offset);
  const rest = await Promise.all(
    offsets.map((offset) => listMembers({ congress, limit: MEMBER_PAGE_SIZE, offset }))
  );
  return [first, ...rest].flatMap((page) => page.items.map((m) => m.bioguide_id));
}

async function currentCongressPages(): Promise<{ bills: BillIndexEntry[]; memberIds: string[] }> {
  const congress = await currentCongress();
  if (congress === undefined) return { bills: [], memberIds: [] };
  const [index, memberIds] = await Promise.all([getBillIndex(congress), congressMemberIds(congress)]);
  return { bills: index.bills, memberIds };
}

export default async function sitemap(): Promise<MetadataRoute.Sitemap> {
  let pages: { bills: BillIndexEntry[]; memberIds: string[] } = { bills: [], memberIds: [] };
  try {
    pages = await currentCongressPages();
  } catch (err) {
    if (process.env.NEXT_PHASE !== BUILD_PHASE) throw err;
    console.warn("sitemap: the API isn't reachable during the build; listing only the shared pages");
  }
  return sitemapEntries(siteOrigin(), pages.bills, pages.memberIds);
}
