import fs from "fs";
import path from "path";
import { describe, expect, it } from "vitest";

import { API_ROUTE_PATTERNS, apiRoutePattern, ROUTE_PATTERNS, routePattern, UNKNOWN_ROUTE } from "../obs/routes";

const APP = path.resolve(__dirname, "../../app");
const API_ROUTER = path.resolve(__dirname, "../../../../api/cmd/server/main.go");

/** The route pattern of every page.tsx under src/app, route groups left out. */
function pageRoutes(dir: string, route: string[] = []): string[] {
  const found: string[] = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      const segment = /^\(.*\)$/.test(entry.name) ? [] : [entry.name];
      found.push(...pageRoutes(path.join(dir, entry.name), [...route, ...segment]));
    } else if (entry.name === "page.tsx") {
      found.push(`/${route.join("/")}`);
    }
  }
  return found;
}

/**
 * The pattern of every route the API registers in its router (`r.Get("/bills/{id}", ...)`,
 * `r.With(...).Post(...)`, `r.Method(http.MethodGet, "/bills", ...)`), all under /api/v1.
 */
function apiRoutes(): string[] {
  const routes = new Set<string>();
  for (const line of fs.readFileSync(API_ROUTER, "utf8").split("\n")) {
    const m =
      /^\s*r(?:\..*)?\.(?:Get|Post|Put|Patch|Delete)\("(\/[^"]*)"/.exec(line) ??
      /^\s*r\.Method\([^,]+, "(\/[^"]*)"/.exec(line);
    if (m) routes.add(`/api/v1${m[1]}`);
  }
  return [...routes];
}

describe("apiRoutePattern", () => {
  it("lists exactly the routes in the API's router", () => {
    const routes = apiRoutes();
    expect(routes).toContain("/api/v1/bills/{id}/vote");
    expect([...API_ROUTE_PATTERNS].sort()).toEqual(routes.sort());
  });

  it.each([
    ["/api/v1/bills", "/api/v1/bills"],
    ["/api/v1/bills/counts", "/api/v1/bills/counts"],
    ["/api/v1/bills/hr-119-1", "/api/v1/bills/{id}"],
    ["/api/v1/bills/hr-119-1/vote", "/api/v1/bills/{id}/vote"],
    ["/api/v1/bills/hr-119-1/text/12", "/api/v1/bills/{id}/text/{vid}"],
    ["/api/v1/me/votes", "/api/v1/me/votes"],
    ["/api/v1/me/votes:import", "/api/v1/me/votes:import"],
    ["/api/v1/members/A000360/positions?congress=119", "/api/v1/members/{id}/positions"],
    ["/api/v1/bills/%7Bid%7D/vote", "/api/v1/bills/{id}/vote"],
    ["/api/v1/bills/hr-119-1/vote/extra", UNKNOWN_ROUTE],
    ["/bills/hr-119-1", UNKNOWN_ROUTE],
  ])("maps %s to %s", (pathname, pattern) => {
    expect(apiRoutePattern(pathname)).toBe(pattern);
  });
});

describe("routePattern", () => {
  it("lists every page in src/app", () => {
    expect([...ROUTE_PATTERNS].sort()).toEqual(pageRoutes(APP).sort());
  });

  it.each([
    ["/", "/"],
    ["/bills", "/bills"],
    ["/bills/119-hr-1", "/bills/[id]"],
    ["/bills/119-hr-1/", "/bills/[id]"],
    ["/members/A000360?tab=votes#x", "/members/[id]"],
    ["/share/bill/119-hr-1/yea/A000360", "/share/bill/[bill]/[vote]/[member]"],
    ["/share/aggregate/hr-119-1/CA-12", "/share/aggregate/[bill]/[scope]"],
    // The scorecard card is gone (#894).
    ["/share/rep/A000360/8-of-12", UNKNOWN_ROUTE],
    ["/vote", "/vote"],
    ["/no/such/page", UNKNOWN_ROUTE],
    ["/bills/119-hr-1/extra", UNKNOWN_ROUTE],
  ])("maps %s to %s", (pathname, pattern) => {
    expect(routePattern(pathname)).toBe(pattern);
  });
});
