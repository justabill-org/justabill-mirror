import type {
  Amendment,
  Bill,
  BillAggregatesResponse,
  BillAction,
  BillDetailResponse,
  BillIndexResponse,
  BillStatusesResponse,
  BillLawChangesResponse,
  BillCardItem,
  BillCounts,
  BillListItem,
  BillListParams,
  BillText,
  BillTextDiff,
  BillTextVersion,
  CollaboratorsResponse,
  CompanionRollCall,
  CompareResponse,
  Congress,
  CongressionalVote,
  DiffDetailResponse,
  GAOReport,
  GraphRelatedBill,
  HealthResponse,
  LawSectionResponse,
  Member,
  MemberAlignmentResponse,
  MemberDetail,
  MemberListParams,
  MemberPositionsResponse,
  PaginatedResult,
  PolicyAreasResponse,
  RepsResponse,
  ScopeAggregateResponse,
  ScorecardResponse,
  User,
  UserFavorite,
  UserVote,
  AccountExport,
  UserVoteChoice,
} from "./types";

import { withServerKey, withVisitor } from "#server-key";
import { appCheckHeaders, needsAppCheck } from "./auth/app-check";

/**
 * The API origin this side of the app calls. The browser always uses NEXT_PUBLIC_API_URL, which
 * `next build` inlines. Server renders prefer API_INTERNAL_URL, read at run time, when it's set:
 * in `task up` the web container's own localhost isn't the API, so docker-compose.yml points it at
 * `http://api:8080` (#649). Vercel leaves it unset, so its servers call the public URL too.
 */
export function apiBaseUrl(urls: { publicUrl?: string; internalUrl?: string }, server: boolean): string {
  if (server && urls.internalUrl) return urls.internalUrl;
  return urls.publicUrl ?? "http://localhost:8080";
}

const API_BASE = apiBaseUrl(
  // Written out in full: Next inlines only a literal `process.env.NEXT_PUBLIC_*` into the bundle.
  { publicUrl: process.env.NEXT_PUBLIC_API_URL, internalUrl: process.env.API_INTERNAL_URL },
  typeof window === "undefined"
);

// ---------------------------------------------------------------------------
// Caching (#74)
// ---------------------------------------------------------------------------

/**
 * How long, in seconds, server renders may reuse a public API response before refetching it.
 * Next.js keeps successful (200) responses in its data cache; errors are never cached. The
 * pipeline syncs hourly at most, so a few minutes of staleness is invisible to readers while
 * an election-week spike hits the cache instead of the API.
 */
export const REVALIDATE = {
  /** Bill lists and the /vote deck: new bills and status changes show within five minutes. */
  list: 300,
  /** One bill's page and its related bills. */
  bill: 600,
  /** Members and the congress list change rarely. */
  member: 3600,
  /** A bill whose detail came back with a section missing (`getBill`): try again soon. */
  partialBill: 60,
} as const;

/**
 * Cache tags, so a later on-demand `revalidateTag` (after a pipeline sync) can drop exactly the
 * responses a change affects.
 */
export const CACHE_TAGS = {
  bills: "bills",
  bill: (id: string) => `bill:${id}`,
  congresses: "congresses",
  members: "members",
  member: (id: string) => `member:${id}`,
} as const;

interface CacheOptions {
  revalidate: number;
  tags: string[];
}

/** A public GET that server renders may cache; in the browser `next` is ignored. */
function cachedGet<T>(path: string, cache: CacheOptions): Promise<T> {
  return request<T>(path, { next: cache });
}

// ---------------------------------------------------------------------------
// Core fetch helper
// ---------------------------------------------------------------------------

class ApiError extends Error {
  constructor(
    public status: number,
    public statusText: string,
    public body: string
  ) {
    super(`API error ${status}: ${statusText}`);
    this.name = "ApiError";
  }

  /** The API's machine-readable error code (e.g. "no_account"), when the body has one. */
  get code(): string | undefined {
    try {
      const parsed: unknown = JSON.parse(this.body);
      if (parsed && typeof parsed === "object" && "code" in parsed && typeof parsed.code === "string") {
        return parsed.code;
      }
    } catch {
      // Not JSON.
    }
    return undefined;
  }
}

/**
 * fetch, traced on the server: a client span and http.client.request.duration per API call, with
 * the trace context sent along (lib/obs/http.ts). The browser gets plain fetch, so the OTel API
 * stays out of the client bundle; browser tracing is loaded lazily on its own (#292).
 *
 * Calls from server components and route handlers also carry the web server's key, so the API
 * gives them their own rate limit instead of the one per-IP bucket shared by everything on
 * Vercel's egress IPs (#270). `#server-key` resolves to the server-only lib/server-key.ts under
 * the `react-server` condition and to lib/no-server-key.ts everywhere else (package.json
 * "imports"), so client components never see the key.
 */
async function apiFetch(url: string, init?: RequestInit): Promise<Response> {
  const keyed = withServerKey(init);
  if (typeof window === "undefined") {
    const { tracedFetch } = await import("./obs/http");
    return tracedFetch(url, keyed);
  }
  return fetch(url, keyed);
}

/**
 * Calls the API. `token` is a Firebase ID token from useUser().getIdToken(), sent as
 * `Authorization: Bearer`: the only way the API identifies a user (#55). Only the browser has
 * one, so signed-in calls never come from a server render.
 */
async function request<T>(
  path: string,
  options: RequestInit & { token?: string } = {}
): Promise<T> {
  const { token, ...init } = options;
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };
  if (token) {
    headers.Authorization = `Bearer ${token}`;
    // The vote and import routes also check that the request comes from our app (#122).
    if (needsAppCheck(init.method, path)) Object.assign(headers, await appCheckHeaders());
  }

  const res = await apiFetch(`${API_BASE}/api/v1${path}`, {
    ...init,
    headers: { ...headers, ...(init.headers as Record<string, string>) },
  });

  if (!res.ok) {
    const body = await res.text();
    throw new ApiError(res.status, res.statusText, body);
  }
  if (res.status === 204) return undefined as T;

  return res.json() as Promise<T>;
}

function qs(params: Record<string, string | number | boolean | undefined | null>): string {
  const parts: string[] = [];
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== "") {
      parts.push(`${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`);
    }
  }
  return parts.length > 0 ? `?${parts.join("&")}` : "";
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

export async function getHealth(): Promise<HealthResponse> {
  const res = await apiFetch(`${API_BASE}/health`);
  return res.json() as Promise<HealthResponse>;
}

// ---------------------------------------------------------------------------
// Bills
// ---------------------------------------------------------------------------

function billListQuery(params: BillListParams): string {
  return qs({
    offset: params.offset,
    limit: params.limit,
    congress: params.congress,
    type: params.type,
    status: typeof params.status === "string" ? params.status : params.status?.join(","),
    status_mode: params.status_mode,
    chamber: params.chamber,
    policy_area: params.policy_area,
    q: params.q,
    sort: params.sort,
    unvoted: params.unvoted ? "true" : undefined,
  });
}

/** Options for `listBills`. */
export interface ListBillsOptions {
  /**
   * Counts a server render's call against the visitor's own rate limit (`withVisitor`, #607): on
   * Vercel it sends their IP and isn't cached. Set only for a filtered `/bills` list, so a view's
   * first page and every other page stay cached and shared.
   */
  visitor?: boolean;
}

/** Lists bills. A user's list (`unvoted` with a token) is personal, so it's never cached. */
export async function listBills(
  params: BillListParams = {},
  token?: string,
  { visitor = false }: ListBillsOptions = {}
): Promise<PaginatedResult<Bill>> {
  const path = `/bills${billListQuery(params)}`;
  // no-store keeps the browser's HTTP cache, keyed by URL alone, from serving it to the next user.
  if (token) return request<PaginatedResult<Bill>>(path, { token, cache: "no-store" });
  const cache: CacheOptions = { revalidate: REVALIDATE.list, tags: [CACHE_TAGS.bills] };
  if (visitor) return request<PaginatedResult<Bill>>(path, await withVisitor({ next: cache }));
  return cachedGet<PaginatedResult<Bill>>(path, cache);
}

/**
 * Lists bills with each one's AI summary and CRS summary lead in the same response
 * (`include=summary,crs_summary`, #714), what a /bills card shows. `visitor` works as in `listBills`.
 */
export async function listBillsWithSummaries(
  params: Omit<BillListParams, "unvoted"> = {},
  { visitor = false }: ListBillsOptions = {}
): Promise<PaginatedResult<BillListItem>> {
  const query = billListQuery(params);
  const path = `/bills${query}${query ? "&" : "?"}include=summary,crs_summary`;
  const cache: CacheOptions = { revalidate: REVALIDATE.list, tags: [CACHE_TAGS.bills] };
  if (visitor) return request<PaginatedResult<BillListItem>>(path, await withVisitor({ next: cache }));
  return cachedGet<PaginatedResult<BillListItem>>(path, cache);
}

/**
 * How many bills the list's filters (congress, type, chamber, policy area, search) match, by
 * current status, in one call (`GET /bills/counts`, #713); paging, sort and status are left out, as
 * the API ignores them. `visitor` works as in `listBills`.
 */
export async function countBills(
  params: Omit<BillListParams, "unvoted"> = {},
  { visitor = false }: ListBillsOptions = {}
): Promise<BillCounts> {
  const path = `/bills/counts${qs({
    congress: params.congress,
    type: params.type,
    chamber: params.chamber,
    policy_area: params.policy_area,
    q: params.q,
  })}`;
  const cache: CacheOptions = { revalidate: REVALIDATE.list, tags: [CACHE_TAGS.bills] };
  if (visitor) return request<BillCounts>(path, await withVisitor({ next: cache }));
  return cachedGet<BillCounts>(path, cache);
}

/**
 * Lists bills with each one's AI summary and /vote card facts (`include=summary,card`, #705): what
 * the deck shows without a call per card. With a token (`unvoted` needs one, #797), the list is the
 * signed-in user's own, so it's never cached.
 */
export async function listBillsWithCards(
  params: BillListParams = {},
  token?: string
): Promise<PaginatedResult<BillCardItem>> {
  const query = billListQuery(token ? params : { ...params, unvoted: undefined });
  const path = `/bills${query}${query ? "&" : "?"}include=summary,card`;
  if (token) return request<PaginatedResult<BillCardItem>>(path, { token, cache: "no-store" });
  return cachedGet<PaginatedResult<BillCardItem>>(path, { revalidate: REVALIDATE.list, tags: [CACHE_TAGS.bills] });
}

/**
 * Every policy area Congress.gov has given a bill, A to Z: what `policy_area` filters by (#708). A
 * new one appears only when a sync brings the first bill on it, so it's cached like the members.
 */
export async function listPolicyAreas(): Promise<PolicyAreasResponse> {
  return cachedGet<PolicyAreasResponse>("/policy-areas", {
    revalidate: REVALIDATE.member,
    tags: [CACHE_TAGS.bills],
  });
}

/** Every bill in the congress, ID and last update only (the sitemap's list). */
export async function getBillIndex(congress: number): Promise<BillIndexResponse> {
  return request<BillIndexResponse>(`/bill-index${qs({ congress })}`);
}

/**
 * Every bill in the congress that passed a chamber or went further, with its status (#853). The
 * same for everyone and cached an hour by the API and the CDN; My votes reads it in the browser
 * and joins it to the visitor's votes there, so no request says which bills they voted on.
 */
export async function getBillStatuses(congress: number): Promise<BillStatusesResponse> {
  return request<BillStatusesResponse>(`/bill-statuses${qs({ congress })}`);
}

/** The list sections of a bill detail. The API sends `[]` for an empty one and null for a failed read. */
const BILL_DETAIL_LISTS = [
  "actions",
  "text_versions",
  "diffs",
  "amendments",
  "votes",
  "status_history",
  "gao_reports",
  "sponsorships",
] as const satisfies readonly (keyof BillDetailResponse)[];

/** Whether a section of a bill detail failed to load: the API still answers 200, with it null. */
export function isPartialBillDetail(detail: BillDetailResponse): boolean {
  return BILL_DETAIL_LISTS.some((section) => detail[section] === null);
}

/**
 * Sent on the second read of a partial bill detail, only so the fetch cache files it apart from
 * the first (the cache key includes the headers). The API ignores it.
 */
export const PARTIAL_BILL_RETRY_HEADER = "X-Partial-Retry";

/**
 * GET /bills/{id}, cached for REVALIDATE.bill. A partial response (#454) is a 200, so the fetch
 * cache keeps it as long as a whole one, and the page would show the gap for ten minutes. So a
 * partial response is read again under its own cache key for REVALIDATE.partialBill: a fresh try
 * now, and since a page's ISR lifetime is its shortest fetch's, the page is rendered again within
 * a minute rather than cached as it is. Once the first entry expires and comes back whole, the
 * second read stops.
 */
export async function getBill(id: string): Promise<BillDetailResponse> {
  const path = `/bills/${id}`;
  const tags = [CACHE_TAGS.bills, CACHE_TAGS.bill(id)];
  const detail = await cachedGet<BillDetailResponse>(path, { revalidate: REVALIDATE.bill, tags });
  if (!isPartialBillDetail(detail)) return detail;
  return request<BillDetailResponse>(path, {
    next: { revalidate: REVALIDATE.partialBill, tags },
    headers: { [PARTIAL_BILL_RETRY_HEADER]: "1" },
  });
}

export async function getBillActions(billId: string): Promise<BillAction[]> {
  return request<BillAction[]>(`/bills/${billId}/actions`);
}

export async function getBillVotes(
  billId: string
): Promise<CongressionalVote[]> {
  return request<CongressionalVote[]>(`/bills/${billId}/votes`);
}

export async function listTextVersions(
  billId: string
): Promise<BillTextVersion[]> {
  return request<BillTextVersion[]>(`/bills/${billId}/text`);
}

export async function getBillText(
  billId: string,
  versionId: string
): Promise<BillText> {
  return request<BillText>(`/bills/${billId}/text/${versionId}`);
}

export async function listDiffs(billId: string): Promise<BillTextDiff[]> {
  return request<BillTextDiff[]>(`/bills/${billId}/diffs`);
}

export async function getDiff(
  billId: string,
  diffId: string
): Promise<DiffDetailResponse> {
  return request<DiffDetailResponse>(`/bills/${billId}/diffs/${diffId}`);
}

export async function listAmendments(billId: string) {
  return request<Amendment[]>(`/bills/${billId}/amendments`);
}

export async function listGAOReports(billId: string): Promise<GAOReport[]> {
  return request<GAOReport[]>(`/bills/${billId}/gao-reports`);
}

export async function getRelatedBills(
  billId: string,
  limit?: number
): Promise<GraphRelatedBill[]> {
  return cachedGet<GraphRelatedBill[]>(`/bills/${billId}/related${qs({ limit })}`, {
    revalidate: REVALIDATE.bill,
    tags: [CACHE_TAGS.bills, CACHE_TAGS.bill(billId)],
  });
}

/** Recorded roll calls on the bill's identical bill in the other chamber, newest first. */
export async function getCompanionVotes(billId: string): Promise<CompanionRollCall[]> {
  return cachedGet<CompanionRollCall[]>(`/bills/${billId}/companion-votes`, {
    revalidate: REVALIDATE.bill,
    tags: [CACHE_TAGS.bills, CACHE_TAGS.bill(billId)],
  });
}

/** The US Code sections the bill's latest text amends, repeals or adds, with AI explanations (#316). */
export async function getBillLawChanges(billId: string): Promise<BillLawChangesResponse> {
  return cachedGet<BillLawChangesResponse>(`/bills/${billId}/law-changes`, {
    revalidate: REVALIDATE.bill,
    tags: [CACHE_TAGS.bills, CACHE_TAGS.bill(billId)],
  });
}

/** A US Code section's current text, fetched from the browser when a reader expands it. */
export async function getLawSection(title: number, section: string): Promise<LawSectionResponse> {
  return request<LawSectionResponse>(`/law/${title}/${encodeURIComponent(section)}`);
}

// ---------------------------------------------------------------------------
// Published aggregates (docs/design/89-aggregate-analytics.md)
// ---------------------------------------------------------------------------

/**
 * How Just a Bill users voted on the bill: national, state and district cells. The API answers
 * 404 (`aggregates_off`) while the feature is off, and the panel then renders nothing. Cached for
 * REVALIDATE.bill rather than the hour the job takes, so turning the feature off clears it soon.
 */
export async function getBillAggregates(billId: string): Promise<BillAggregatesResponse> {
  return cachedGet<BillAggregatesResponse>(`/bills/${billId}/aggregates`, {
    revalidate: REVALIDATE.bill,
    tags: [CACHE_TAGS.bills, CACHE_TAGS.bill(billId)],
  });
}

/** One state's or district's cell and its members' votes on the bill, fetched from the browser. */
export async function getScopeAggregate(billId: string, scopeKey: string): Promise<ScopeAggregateResponse> {
  return request<ScopeAggregateResponse>(`/bills/${billId}/aggregates/${encodeURIComponent(scopeKey)}`);
}

/** How often users in the member's constituency agreed with them, per congress (browser). */
export async function getMemberAlignment(memberId: string): Promise<MemberAlignmentResponse> {
  return request<MemberAlignmentResponse>(`/members/${memberId}/alignment`);
}

// ---------------------------------------------------------------------------
// Congresses
// ---------------------------------------------------------------------------

export async function listCongresses(): Promise<Congress[]> {
  return cachedGet<Congress[]>("/congresses", {
    revalidate: REVALIDATE.member,
    tags: [CACHE_TAGS.congresses],
  });
}

// ---------------------------------------------------------------------------
// Members
// ---------------------------------------------------------------------------

export async function listMembers(
  params: MemberListParams = {}
): Promise<PaginatedResult<Member>> {
  const query = qs({
    offset: params.offset,
    limit: params.limit,
    congress: params.congress,
    chamber: params.chamber,
    state: params.state,
    district: params.district,
  });
  return cachedGet<PaginatedResult<Member>>(`/members${query}`, {
    revalidate: REVALIDATE.member,
    tags: [CACHE_TAGS.members],
  });
}

export async function getMember(id: string): Promise<MemberDetail> {
  return cachedGet<MemberDetail>(`/members/${id}`, {
    revalidate: REVALIDATE.member,
    tags: [CACHE_TAGS.members, CACHE_TAGS.member(id)],
  });
}

export async function getCollaborators(
  memberId: string,
  params: { congress?: number; limit?: number } = {}
): Promise<CollaboratorsResponse> {
  const query = qs({ congress: params.congress, limit: params.limit });
  return cachedGet<CollaboratorsResponse>(`/members/${memberId}/collaborators${query}`, {
    revalidate: REVALIDATE.member,
    tags: [CACHE_TAGS.members, CACHE_TAGS.member(memberId)],
  });
}

/** The member's positions on bills in one congress (#72's public, cacheable endpoint). */
export async function getMemberPositions(
  memberId: string,
  congress: number
): Promise<MemberPositionsResponse> {
  return cachedGet<MemberPositionsResponse>(`/members/${memberId}/positions${qs({ congress })}`, {
    revalidate: REVALIDATE.list,
    tags: [CACHE_TAGS.members, CACHE_TAGS.member(memberId)],
  });
}

/**
 * Finds the representatives and senators for a street address. The address goes in a POST body,
 * never the URL, so it stays out of request logs and browser history (#268).
 */
export async function getReps(address: string): Promise<RepsResponse> {
  return request<RepsResponse>("/reps", {
    method: "POST",
    body: JSON.stringify({ address }),
  });
}

/**
 * Finds the representatives and senators at a point, such as the browser's location (#711). Like
 * an address, the latitude and longitude go only in the POST body.
 */
export async function getRepsAt(lat: number, lon: number): Promise<RepsResponse> {
  return request<RepsResponse>("/reps", {
    method: "POST",
    body: JSON.stringify({ lat, lon }),
  });
}

// ---------------------------------------------------------------------------
// Account (signed in: every call takes an ID token)
// ---------------------------------------------------------------------------

/** Creates the account for a signed-in user, or returns the existing one (201 or 200). */
export async function createMe(token: string): Promise<User> {
  return request<User>("/me", { token, method: "POST" });
}

/** The signed-in user's account; 403 with code "no_account" until createMe has run. */
export async function getMe(token: string): Promise<User> {
  return request<User>("/me", { token });
}

/**
 * Sets the account's state and district, the only profile fields the API keeps. It answers 429
 * with code "district_change_limited" when they were changed too recently.
 */
export async function updateMe(
  token: string,
  data: { state: string; district: number }
): Promise<User> {
  return request<User>("/me", {
    token,
    method: "PATCH",
    body: JSON.stringify(data),
  });
}

/**
 * Everything the API keeps about the signed-in user (profile, votes and followed bills), for
 * "Download my data". Returned as the API sent it, so the download is exactly what's stored.
 */
export async function exportMe(token: string): Promise<AccountExport> {
  return request<AccountExport>("/me/export", { token });
}

/**
 * Deletes the account: the row, its votes and followed bills, and the sign-in itself. The API
 * answers 401 with code "requires_recent_login" unless the user signed in within five minutes.
 */
export async function deleteMe(token: string): Promise<void> {
  await request<void>("/me", { token, method: "DELETE" });
}

/** What POST /me/votes:import did. The capped fields are absent from APIs before #458. */
export interface ImportVotesResult {
  imported: number;
  /** Votes on bills the account had already voted on (it wins), or that the API doesn't know. */
  skipped: number;
  /** Votes left out by the daily vote cap: not stored, so send them again later. */
  capped?: number;
  capped_bill_ids?: string[];
}

/**
 * Adds votes cast before signing in to the account, at most MAX_IMPORT_VOTES per call. Votes
 * already in the account win, and votes on bills the API doesn't know are skipped. Imported votes
 * count toward the daily vote cap: the API stores the first ones that fit and lists the rest.
 */
export async function importMyVotes(
  token: string,
  votes: readonly { bill_id: string; vote: UserVoteChoice; voted_at: string }[]
): Promise<ImportVotesResult> {
  return request("/me/votes:import", { token, method: "POST", body: JSON.stringify({ votes }) });
}

/** POST /me/votes:import's batch limit (db/model.MaxImportVotes). */
export const MAX_IMPORT_VOTES = 1000;

export async function castVote(
  token: string,
  billId: string,
  vote: UserVoteChoice
): Promise<{ bill_id: string; vote: string; status: string }> {
  return request(`/bills/${billId}/vote`, {
    token,
    method: "POST",
    body: JSON.stringify({ vote }),
  });
}

/**
 * Removes the signed-in user's vote on a bill (#458). The API answers 204 also when there was no
 * vote, so repeating it is safe. No App Check: the API checks only where votes are cast.
 */
export async function deleteVote(token: string, billId: string): Promise<void> {
  await request<void>(`/bills/${billId}/vote`, { token, method: "DELETE" });
}

export async function getMyVotes(
  token: string,
  params: { offset?: number; limit?: number } = {}
): Promise<PaginatedResult<UserVote>> {
  const query = qs({ offset: params.offset, limit: params.limit });
  return request<PaginatedResult<UserVote>>(`/me/votes${query}`, { token });
}

export async function getMyFavorites(
  token: string,
  params: { offset?: number; limit?: number } = {}
): Promise<PaginatedResult<UserFavorite>> {
  const query = qs({ offset: params.offset, limit: params.limit });
  return request<PaginatedResult<UserFavorite>>(`/me/favorites${query}`, { token });
}

export async function addFavorite(
  token: string,
  billId: string
): Promise<{ bill_id: string; status: string }> {
  return request(`/me/favorites/${billId}`, { token, method: "POST" });
}

export async function removeFavorite(
  token: string,
  billId: string
): Promise<{ bill_id: string; status: string }> {
  return request(`/me/favorites/${billId}`, { token, method: "DELETE" });
}

/** The signed-in scorecard, limited to `congresses` (#242's repeatable `?congress=`); all when empty. */
export async function getScorecard(
  token: string,
  congresses: readonly number[] = []
): Promise<ScorecardResponse> {
  const query = new URLSearchParams(congresses.map((c) => ["congress", String(c)])).toString();
  return request<ScorecardResponse>(`/me/scorecard${query ? `?${query}` : ""}`, { token });
}

export async function compareMember(
  token: string,
  memberId: string
): Promise<CompareResponse> {
  return request<CompareResponse>(`/me/compare/${memberId}`, { token });
}

// ---------------------------------------------------------------------------
// Re-export types for convenience
// ---------------------------------------------------------------------------

export { ApiError };
export type { BillListParams, MemberListParams };
