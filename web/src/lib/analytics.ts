// Vercel Web Analytics (#156). Design: docs/design/51-vercel.md ("Analytics").
//
// Cookieless page views, only on the production deployment. The script and its
// beacon are same-origin (/_vercel/insights/*), so the CSP in security-headers.ts
// needs no new source.

import { track } from "@vercel/analytics";
import type { BeforeSendEvent } from "@vercel/analytics/next";
import type { ShareChannel, ShareKind } from "./share-links";

type AnalyticsEnv = Record<string, string | undefined>;

// Query parameters worth keeping: where a visitor came from. Everything else is
// dropped, e.g. the bill search box's free text (?q=), which can hold anything a
// visitor types.
const KEPT_PARAMS = new Set(["ref", "utm_source", "utm_medium", "utm_campaign"]);

// True only on Vercel's production deployment: not previews, local runs, compose
// or Playwright, so tests and reviewers never add events.
export function analyticsEnabled(env: AnalyticsEnv): boolean {
  return env.VERCEL_ENV === "production";
}

// Share card paths carry the sharer's vote or counts (#88), or a place that may be
// the sharer's own (#166), so analytics keep only their route pattern:
// /share/rep/[member]/[score], /share/bill/[bill]/[vote],
// /share/bill/[bill]/[vote]/[member] and /share/aggregate/[bill]/[scope], with
// /image.png kept when present.
const SHARE_ROUTES: [RegExp, string][] = [
  [/^\/share\/aggregate\/[^/]+\/[^/]+(\/image\.png)?\/?$/, "/share/aggregate/[bill]/[scope]"],
  [/^\/share\/rep\/[^/]+\/[^/]+(\/image\.png)?\/?$/, "/share/rep/[member]/[score]"],
  [/^\/share\/bill\/[^/]+\/[^/]+\/(?!image\.png)[^/]+(\/image\.png)?\/?$/, "/share/bill/[bill]/[vote]/[member]"],
  [/^\/share\/bill\/[^/]+\/[^/]+(\/image\.png)?\/?$/, "/share/bill/[bill]/[vote]"],
];

// The route pattern for a /share/... path; anything else under /share/ becomes
// "/share", and other paths are returned as they are.
export function redactSharePath(pathname: string): string {
  if (!pathname.startsWith("/share/")) return pathname;
  for (const [pattern, route] of SHARE_ROUTES) {
    const m = pattern.exec(pathname);
    if (m) return `${route}${m[1] ?? ""}`;
  }
  return "/share";
}

// beforeSend for <Analytics />: keeps the path (share paths as their route
// pattern) and the campaign parameters, and drops the rest of the query string
// and the fragment.
export function redactAnalyticsEvent(event: BeforeSendEvent): BeforeSendEvent {
  let url: URL;
  try {
    url = new URL(event.url);
  } catch {
    // The script sends absolute URLs; if one isn't, drop the whole query.
    return { ...event, url: redactSharePath(event.url.split(/[?#]/)[0]) };
  }
  url.pathname = redactSharePath(url.pathname);
  for (const key of [...url.searchParams.keys()]) {
    if (!KEPT_PARAMS.has(key)) url.searchParams.delete(key);
  }
  url.hash = "";
  return { ...event, url: url.toString() };
}

// The share dialog's one custom event: which kind of card and how it was shared,
// never the card's URL, member or counts. A no-op unless <Analytics /> loaded the
// script, so it sends nothing outside production.
export function trackShare(kind: ShareKind, channel: ShareChannel, send: typeof track = track): void {
  send("share", { kind, channel });
}
