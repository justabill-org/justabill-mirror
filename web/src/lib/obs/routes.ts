// The app's and the API's route patterns, for browser telemetry (design
// docs/design/53-observability.md: URLs are reduced to route patterns). The browser has no API for
// the current route's pattern, so these lists mirror the page.tsx files under src/app and the
// routes in api/cmd/server/main.go; obs-routes.test.ts fails when they drift apart.

/** Every page route, as Next.js writes its pattern (route groups left out). */
export const ROUTE_PATTERNS = [
  "/",
  "/about",
  "/bills",
  "/bills/[id]",
  "/contact",
  "/login",
  "/members/[id]",
  "/methodology",
  "/my-votes",
  "/privacy",
  "/scorecard",
  "/settings",
  "/share/aggregate/[bill]/[scope]",
  "/share/bill/[bill]/[vote]",
  "/share/bill/[bill]/[vote]/[member]",
  "/signup",
  "/terms",
  "/vote",
  "/vote/v/[variant]",
] as const;

/**
 * Every API route, as chi writes its pattern in api/cmd/server/main.go, whatever its method: the
 * same string as the API's own `http.route`, so a browser fetch span reads like the API span it
 * links to. obs-routes.test.ts checks it against the router, and a change to main.go runs those
 * web tests (ci-changes.sh, #767), so the list matches the router exactly.
 */
export const API_ROUTE_PATTERNS = [
  "/api/v1/bill-index",
  "/api/v1/bill-statuses",
  "/api/v1/bills",
  "/api/v1/bills/counts",
  "/api/v1/bills/{id}",
  "/api/v1/bills/{id}/actions",
  "/api/v1/bills/{id}/aggregates",
  "/api/v1/bills/{id}/aggregates/{scope_key}",
  "/api/v1/bills/{id}/amendments",
  "/api/v1/bills/{id}/companion-votes",
  "/api/v1/bills/{id}/diffs",
  "/api/v1/bills/{id}/diffs/{did}",
  "/api/v1/bills/{id}/gao-reports",
  "/api/v1/bills/{id}/law-changes",
  "/api/v1/bills/{id}/related",
  "/api/v1/bills/{id}/text",
  "/api/v1/bills/{id}/text/{vid}",
  "/api/v1/bills/{id}/vote",
  "/api/v1/bills/{id}/votes",
  "/api/v1/congresses",
  "/api/v1/law/{title}/{section}",
  "/api/v1/me",
  "/api/v1/me/compare/{memberID}",
  "/api/v1/me/export",
  "/api/v1/me/favorites",
  "/api/v1/me/favorites/{billID}",
  "/api/v1/me/scorecard",
  "/api/v1/me/votes",
  "/api/v1/me/votes:import",
  "/api/v1/members",
  "/api/v1/members/{id}",
  "/api/v1/members/{id}/alignment",
  "/api/v1/members/{id}/collaborators",
  "/api/v1/members/{id}/positions",
  "/api/v1/policy-areas",
  "/api/v1/reps",
] as const;

/** What a path that matches no page or API route reports, so an unknown URL never reaches telemetry. */
export const UNKNOWN_ROUTE = "/[unknown]";

function isParam(part: string): boolean {
  return part.startsWith("[") || part.startsWith("{");
}

function matches(pattern: string, segments: string[]): boolean {
  const parts = pattern.split("/").filter(Boolean);
  if (parts.length !== segments.length) return false;
  return parts.every((p, i) => isParam(p) || p === segments[i]);
}

// The first pattern that matches pathname's segments, a static one before a dynamic one, or
// UNKNOWN_ROUTE. A query string or fragment is ignored.
function bestMatch(patterns: readonly string[], pathname: string): string {
  const path = pathname.split(/[?#]/)[0];
  const segments = path.split("/").filter(Boolean);
  let dynamic: string | undefined;
  for (const pattern of patterns) {
    if (!matches(pattern, segments)) continue;
    if (!pattern.split("/").some(isParam)) return pattern;
    dynamic ??= pattern;
  }
  return dynamic ?? UNKNOWN_ROUTE;
}

/**
 * The page route pattern for a pathname (`/bills/119-hr-1` → `/bills/[id]`), preferring a static
 * route over a dynamic one. A query string or fragment is ignored; an unknown path is
 * UNKNOWN_ROUTE.
 */
export function routePattern(pathname: string): string {
  return bestMatch(ROUTE_PATTERNS, pathname);
}

/**
 * The API route pattern for a pathname on the API's origin (`/api/v1/bills/hr-119-1/vote` →
 * `/api/v1/bills/{id}/vote`), preferring a static route over a dynamic one; an unknown path is
 * UNKNOWN_ROUTE.
 */
export function apiRoutePattern(pathname: string): string {
  return bestMatch(API_ROUTE_PATTERNS, pathname);
}
