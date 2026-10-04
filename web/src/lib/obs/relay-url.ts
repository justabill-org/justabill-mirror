// URL scrubbing shared by the browser SDK and the relay. Its own module so that the browser's
// lazy chunk doesn't pull in the relay's server-side code.

import { apiRoutePattern, routePattern, UNKNOWN_ROUTE } from "./routes";

/** The API's origin from NEXT_PUBLIC_API_URL, with the same local default as lib/api.ts. */
export function apiOriginOf(apiUrl: string | undefined): string {
  return new URL(apiUrl || "http://localhost:8080").origin;
}

/**
 * Reduces a URL to what telemetry may keep (design docs/design/53-observability.md: route
 * patterns only, never which bill or member):
 * - on the API's origin, the API route pattern (`https://api.justabill.io/api/v1/bills/{id}/vote`);
 * - on our own origin, the page route pattern (`https://justabill.io/bills/[id]`);
 * - on any other origin, the origin alone (`https://www.congress.gov/`).
 *
 * The relay can't tell which origin the browser was on, so it passes no `origin`: a URL off the
 * API's origin then keeps its path only if that is a page pattern (what the browser sent for our
 * own pages), and otherwise just its origin. Undefined for anything that isn't an http(s) URL.
 * Reducing a reduced URL changes nothing.
 */
export function reduceUrl(url: string, origin: string | undefined, apiOrigin?: string): string | undefined {
  let u: URL;
  try {
    u = new URL(url, origin);
  } catch {
    return undefined;
  }
  if (u.protocol !== "http:" && u.protocol !== "https:") return undefined;
  if (u.origin === apiOrigin) return `${u.origin}${apiRoutePattern(u.pathname)}`;
  if (u.origin === origin) return `${u.origin}${routePattern(u.pathname)}`;
  if (origin === undefined) {
    const page = routePattern(u.pathname);
    if (page !== UNKNOWN_ROUTE || u.pathname === UNKNOWN_ROUTE) return `${u.origin}${page}`;
  }
  return `${u.origin}/`;
}
