import { type BillSort, type BillViewKey, DEFAULT_BILL_VIEW, parseBillSort, parseBillView } from "@/lib/bill-views";
import { ALL_CONGRESSES } from "@/lib/congress";
import { clampSearch, parseCongress } from "@/lib/paging";
import { BILL_TYPE_LABELS, type BillListParams, type BillType } from "@/lib/types";

// The filters /bills and /vote share (#785): one model, read from and written to the URL the same
// way on both pages, so `/bills?show=passed&type=hr` and `/vote?show=passed&type=hr` are the same
// bills. A plain module, so server pages and client controls share it.

/** The most characters (code points) the API takes in a policy area (`maxPolicyAreaRunes`). */
export const MAX_POLICY_AREA_CHARS = 100;

export type BillChamber = "house" | "senate";

export interface BillFilters {
  view: BillViewKey;
  sort: BillSort;
  /** A congress's number, every congress, or undefined for the current one. */
  congress: number | typeof ALL_CONGRESSES | undefined;
  type?: BillType;
  /** The chamber the bill started in. */
  chamber?: BillChamber;
  /** A policy area's name, as `GET /policy-areas` lists them. */
  area?: string;
  q?: string;
}

/** The list parameters the filters set: everything but the statuses, the page and `unvoted`. */
export type BillFilterParams = Omit<BillListParams, "offset" | "limit" | "status" | "status_mode" | "unvoted">;

/** A page's `searchParams` as URLSearchParams, keeping the first value of a repeated parameter. */
export function searchParamsOf(record: Record<string, string | string[] | undefined>): URLSearchParams {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(record)) {
    const first = Array.isArray(value) ? value[0] : value;
    if (first !== undefined) params.set(key, first);
  }
  return params;
}

function parseCongressFilter(raw: string | null): BillFilters["congress"] {
  return raw === ALL_CONGRESSES ? ALL_CONGRESSES : parseCongress(raw);
}

function parseType(raw: string | null): BillType | undefined {
  return raw && Object.hasOwn(BILL_TYPE_LABELS, raw) ? (raw as BillType) : undefined;
}

function parseChamber(raw: string | null): BillChamber | undefined {
  return raw === "house" || raw === "senate" ? raw : undefined;
}

/**
 * An area cut to what the API takes. An unknown area is kept: it matches no bill, so the page says
 * "No bills found" rather than quietly listing every area.
 */
function parseArea(raw: string | null): string | undefined {
  const area = [...(raw ?? "")].slice(0, MAX_POLICY_AREA_CHARS).join("");
  return area || undefined;
}

/**
 * The filters a URL asks for: `show`, `sort`, `congress`, `type`, `chamber`, `area` and `q`. A value
 * that is missing or unknown falls back to its default, so a hand-edited or stale URL shows a list,
 * and nothing it holds makes the API answer 400.
 */
export function parseBillFilters(params: URLSearchParams): BillFilters {
  const f: BillFilters = {
    view: parseBillView(params.get("show") ?? undefined).key,
    sort: parseBillSort(params.get("sort") ?? undefined),
    congress: parseCongressFilter(params.get("congress")),
  };
  const type = parseType(params.get("type"));
  const chamber = parseChamber(params.get("chamber"));
  const area = parseArea(params.get("area"));
  const q = clampSearch(params.get("q") ?? undefined);
  if (type) f.type = type;
  if (chamber) f.chamber = chamber;
  if (area) f.area = area;
  if (q) f.q = q;
  return f;
}

/** The filters as URL parameters, in a fixed order, leaving out the defaults. */
export function billFiltersSearchParams(f: BillFilters): URLSearchParams {
  const params = new URLSearchParams();
  if (f.view !== DEFAULT_BILL_VIEW) params.set("show", f.view);
  if (f.sort !== "latest_action") params.set("sort", f.sort);
  if (f.congress !== undefined) params.set("congress", String(f.congress));
  if (f.type) params.set("type", f.type);
  if (f.chamber) params.set("chamber", f.chamber);
  if (f.area) params.set("area", f.area);
  if (f.q) params.set("q", f.q);
  return params;
}

/** The filters as a query string without the `?`: empty for the defaults. */
export function billFiltersQuery(f: BillFilters): string {
  return billFiltersSearchParams(f).toString();
}

/** The list parameters for the filters; `current` is the congress in session, when it's known. */
export function billListParams(f: BillFilters, current: number | undefined): BillFilterParams {
  const params: BillFilterParams = { sort: f.sort };
  const congress = f.congress === ALL_CONGRESSES ? undefined : (f.congress ?? current);
  if (congress !== undefined) params.congress = congress;
  if (f.type) params.type = f.type;
  if (f.chamber) params.chamber = f.chamber;
  if (f.area) params.policy_area = f.area;
  if (f.q) params.q = f.q;
  return params;
}

/** `/vote` with the same filters: the bills of a /bills list, one card at a time. */
export function voteHref(f: BillFilters): string {
  const query = billFiltersQuery(f);
  return query ? `/vote?${query}` : "/vote";
}
