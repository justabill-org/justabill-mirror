import { congressGovUrl } from "@/lib/seo";
import type { Bill, BillTextVersion, CrsSummary } from "@/lib/types";

/** About how much of a CRS summary shows before "Read more". */
export const CRS_PREVIEW_CHARS = 600;

/** The summary's paragraphs, split at blank lines. */
export function crsParagraphs(text: string): string[] {
  return text
    .split(/\n\s*\n/)
    .map((p) => p.trim())
    .filter(Boolean);
}

/** The first paragraph: the bill's schema.org abstract. */
export function crsAbstract(text: string): string | undefined {
  return crsParagraphs(text)[0];
}

/**
 * The text shown collapsed: whole paragraphs up to about `limit` characters, and always at least the
 * first. `truncated` says whether anything was left out.
 */
export function crsPreview(text: string, limit = CRS_PREVIEW_CHARS): { preview: string; truncated: boolean } {
  const paragraphs = crsParagraphs(text);
  let kept = 0;
  let length = 0;
  for (const p of paragraphs) {
    const next = length + (kept > 0 ? 2 : 0) + p.length;
    if (kept > 0 && next > limit) break;
    length = next;
    kept++;
  }
  return { preview: paragraphs.slice(0, kept).join("\n\n"), truncated: kept < paragraphs.length };
}

/** This summary's page on Congress.gov, e.g. …/119th-congress/senate-bill/5/summary/00. */
export function crsSummaryUrl(
  bill: Pick<Bill, "congress" | "bill_type" | "number">,
  summary: Pick<CrsSummary, "version_code">,
): string | undefined {
  const base = congressGovUrl(bill);
  return base ? `${base}/summary/${encodeURIComponent(summary.version_code)}` : undefined;
}

/**
 * Whether the bill has a text version dated after the action the summary describes. Dates compare by
 * day, so the text of the action the summary describes doesn't count as newer.
 */
export function billChangedSince(summary: Pick<CrsSummary, "action_date">, versions: BillTextVersion[]): boolean {
  const summaryDay = summary.action_date.slice(0, 10);
  return versions.some((v) => v.date !== undefined && v.date.slice(0, 10) > summaryDay);
}
