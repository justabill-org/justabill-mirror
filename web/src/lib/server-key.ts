// The web server's key for the API (#270, design docs/design/71-api-hardening.md). Server renders
// on Vercel share egress IPs with everyone else's, so without it one per-IP rate limit would cover
// every visitor behind the same IP. With it, the API counts our server-side calls in a bucket of
// their own. The key only raises that limit; it grants no data access.
//
// `server-only` makes the build fail if a client component ever imports this module, and the key
// is API_SERVER_KEY, never NEXT_PUBLIC_, so it isn't inlined into the browser bundle either.
import "server-only";

import { headers as requestHeaders } from "next/headers";

/** The header the API reads the key from (API_SERVER_KEYS on the API side). */
export const SERVER_KEY_HEADER = "X-Server-Key";

/** The header the API reads a visitor's IP from, honored only next to a valid key (#607). */
export const VISITOR_IP_HEADER = "X-Visitor-IP";

type Env = Record<string, string | undefined>;

/** A request's headers as a plain record, so the caller's header names stay as given. */
function headerRecord(init: RequestInit): Record<string, string> {
  return init.headers instanceof Headers || Array.isArray(init.headers)
    ? Object.fromEntries(new Headers(init.headers))
    : { ...init.headers };
}

/**
 * Returns `init` with `X-Server-Key` set from API_SERVER_KEY, or `init` unchanged when the
 * variable is unset or blank, so the API just limits the call by IP. A plain record of headers
 * keeps its keys as given; a Headers object or a list of pairs is converted to one.
 */
export function withServerKey(init: RequestInit = {}, env: Env = process.env): RequestInit {
  const key = env.API_SERVER_KEY?.trim();
  if (!key) return init;
  return { ...init, headers: { ...headerRecord(init), [SERVER_KEY_HEADER]: key } };
}

/**
 * Returns `init` with `X-Visitor-IP` set to the visitor's address and `cache: "no-store"` (any
 * `next` caching dropped), so the API counts the call against that visitor's own allowance rather
 * than the web server's shared one (#607, docs/design/607-per-visitor-web-limits.md). Such a call
 * is the visitor's own and is never cached.
 *
 * It reads `x-real-ip` only on Vercel (`VERCEL=1`), which overwrites that header with the
 * connecting address, so a visitor can't choose it. Anywhere else (local dev, compose), or with no
 * address, it returns `init` unchanged. Only a render that is per request anyway may call it:
 * `headers()` makes the page dynamic. Today that is the filtered `/bills` list alone.
 */
export async function withVisitor(init: RequestInit = {}, env: Env = process.env): Promise<RequestInit> {
  if (env.VERCEL !== "1") return init;
  const ip = (await requestHeaders()).get("x-real-ip");
  if (!ip) return init;
  const uncached: RequestInit = { ...init, cache: "no-store" };
  delete uncached.next;
  return { ...uncached, headers: { ...headerRecord(init), [VISITOR_IP_HEADER]: ip } };
}
