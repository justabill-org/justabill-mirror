import type { BillCounts, BillListItem, BillStatus } from "@/lib/types";

// The quick views on /bills (#666): Laws, Passed a chamber, In committee and All. A plain module,
// so the server page and the client controls share it.

export type BillViewKey = "laws" | "passed" | "committee" | "all";

export interface BillView {
  key: BillViewKey;
  label: string;
  /** The current statuses the view lists; empty for every bill. The views don't overlap. */
  statuses: BillStatus[];
}

/** The views in the order the chips show them. Laws come first and are the default. */
export const BILL_VIEWS: BillView[] = [
  { key: "laws", label: "Laws", statuses: ["became_law", "signed"] },
  {
    key: "passed",
    label: "Passed a chamber",
    statuses: ["passed_house", "passed_senate", "resolving_differences", "to_president", "vetoed"],
  },
  { key: "committee", label: "In committee", statuses: ["in_committee"] },
  { key: "all", label: "All", statuses: [] },
];

export const DEFAULT_BILL_VIEW: BillViewKey = "laws";

/** The view named by `?show=`, or the default when it's missing or unknown. */
export function parseBillView(raw: string | undefined): BillView {
  return BILL_VIEWS.find((v) => v.key === raw) ?? BILL_VIEWS.find((v) => v.key === DEFAULT_BILL_VIEW)!;
}

export type BillSort = "latest_action" | "introduced_date";

/** The sort choices: the API orders "Latest action" by the latest action's date (#712). */
export const BILL_SORTS: { value: BillSort; label: string }[] = [
  { value: "latest_action", label: "Latest action" },
  { value: "introduced_date", label: "Date introduced" },
];

/** The sort `?sort=` asks for: Date introduced, or else Latest action (old `updated_at` links included). */
export function parseBillSort(raw: string | undefined): BillSort {
  return raw === "introduced_date" ? "introduced_date" : "latest_action";
}

/**
 * The `status` filter of a view's list: its statuses in one call (the API takes several, #712), or
 * none for every bill.
 */
export function viewStatuses(view: BillView): BillStatus[] | undefined {
  return view.statuses.length ? view.statuses : undefined;
}

/**
 * Each view's number of bills, from the one `GET /bills/counts` answer (#713): a view's statuses
 * summed, and All the total, which also holds the bills with no status yet.
 */
export function viewCounts(counts: BillCounts): Record<BillViewKey, number> {
  const entries = BILL_VIEWS.map((v) => {
    const n = v.statuses.length ? v.statuses.reduce((sum, s) => sum + (counts.by_status[s] ?? 0), 0) : counts.total;
    return [v.key, n] as const;
  });
  return Object.fromEntries(entries) as Record<BillViewKey, number>;
}

/** The steps of the compact progress indicator on a bill card. */
export const BILL_STEPS = ["Introduced", "Passed one chamber", "Passed both chambers", "Became law"] as const;

/**
 * How many of BILL_STEPS a bill's current status shows it has reached (1 to 4). A Senate bill
 * that also passed the House can show "passed_senate", so a passed status counts as one chamber:
 * the indicator never claims more than the status says.
 */
export function billStep(status: BillStatus | undefined): number {
  switch (status) {
    case "became_law":
    case "signed":
      return 4;
    case "resolving_differences":
    case "to_president":
    case "vetoed":
      return 3;
    case "passed_house":
    case "passed_senate":
      return 2;
    default:
      return 1;
  }
}

// Words that end with a period but don't end a sentence in bill summaries.
const ABBREVIATIONS = new Set([
  "u.s", "h.r", "s", "res", "con", "j", "sec", "secs", "no", "nos", "mr", "mrs", "ms", "dr", "jr", "sr", "st",
  "inc", "co", "corp", "gen", "gov", "rep", "sen", "pub", "stat", "l", "vs", "e.g", "i.e", "etc", "approx", "dept",
  "jan", "feb", "mar", "apr", "jun", "jul", "aug", "sep", "sept", "oct", "nov", "dec",
]);

/** The first sentence of a summary, or the whole text when it has one sentence. */
export function firstSentence(text: string): string {
  const t = text.replace(/\s+/g, " ").trim();
  const re = /[.!?](?=\s+["“(]?[A-Z0-9])/g;
  for (let m = re.exec(t); m; m = re.exec(t)) {
    const before = t.slice(0, m.index);
    const word = before.slice(before.lastIndexOf(" ") + 1).replace(/^[("“]/, "").toLowerCase();
    if (m[0] === "." && (ABBREVIATIONS.has(word) || /^[a-z]$/.test(word))) continue;
    return t.slice(0, m.index + 1);
  }
  return t;
}

/** The card's one-line description and where it comes from. */
export interface BillDescription {
  /** "AI summary" for our own summary, "CRS summary" for the Congressional Research Service's. */
  label: "AI summary" | "CRS summary";
  text: string;
}

/**
 * The card's one-line description: the first sentence of the AI summary, or else of the latest
 * CRS summary's lead (`include=crs_summary`, #714), or null when the bill has neither.
 */
export function billDescription(bill: BillListItem): BillDescription | null {
  const ai = bill.summary?.short_summary?.trim();
  if (ai) return { label: "AI summary", text: firstSentence(ai) };
  const crs = bill.crs_summary?.lead?.trim();
  if (crs) return { label: "CRS summary", text: firstSentence(crs) };
  return null;
}
