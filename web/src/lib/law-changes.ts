import type { LawChangeEntry, LawChangeKind } from "./types";

// Helpers for the "Changes to current law" panel (#316, docs/design/149-law-aware-assistant.md).
// Plain module: the server panel and the client section text both import it.

/** The section ID prefix of a law a bill names without a US Code citation (db/model/law.go). */
export const NON_USC_PREFIX = "nonusc:";

export const LAW_CHANGE_KIND_LABELS: Record<LawChangeKind, string> = {
  amends: "Amends",
  repeals: "Repeals",
  adds: "Adds",
};

/**
 * "42 U.S.C. 1395w-4" for a US Code section, "10 U.S.C. 4271 note" for a statutory note under one;
 * for a nonusc: law, the name the bill uses.
 */
export function lawCitation(entry: LawChangeEntry): string {
  if (entry.in_us_code && entry.title_number !== null && entry.section_number) {
    const citation = `${entry.title_number} U.S.C. ${entry.section_number}`;
    return entry.is_note ? `${citation} note` : citation;
  }
  const name = entry.section_id.startsWith(NON_USC_PREFIX)
    ? entry.section_id.slice(NON_USC_PREFIX.length)
    : entry.section_id;
  return name || entry.cite_text || entry.section_id;
}

/**
 * The section on uscode.house.gov, the Law Revision Counsel's site the text is loaded from, e.g.
 * https://uscode.house.gov/view.xhtml?req=granuleid:USC-prelim-title42-section1395w-4&num=0&edition=prelim
 */
export function uscodeHouseUrl(title: number, section: string): string {
  const granule = `USC-prelim-title${title}-section${section}`;
  return `https://uscode.house.gov/view.xhtml?req=granuleid:${encodeURIComponent(granule)}&num=0&edition=prelim`;
}

/** "Public Law 119-111" for release point "119-111". */
export function publicLawName(releasePoint: string): string {
  return `Public Law ${releasePoint}`;
}
