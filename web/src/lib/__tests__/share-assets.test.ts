import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { CARD_ASSET_FILES, CARD_ROUTE_GLOBS, cardTracingIncludes } from "../share-assets";

// A file the card reads but a Vercel function bundle lacks breaks every link preview from that
// route, and only on Vercel (#706). These tests keep next.config.ts's tracing includes in step
// with the routes and files.

type Matcher = (glob: string, options?: { dot?: boolean; contains?: boolean }) => (input: string) => boolean;
// The picomatch build Next itself matches the globs with (collect-build-traces.js); it ships no types.
const picomatch = (await import("next/dist/compiled/picomatch" as string)).default as Matcher;

const webRoot = path.resolve(__dirname, "../../..");
const appDir = path.join(webRoot, "src/app");

function filesUnder(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true, recursive: true })
    .filter((entry) => entry.isFile())
    .map((entry) => path.join(entry.parentPath, entry.name));
}

/** Route files that render the card: they call shareImageResponse or re-export a card route. */
function cardRouteFiles(): string[] {
  return filesUnder(appDir).filter((file) => {
    if (!/\.tsx?$/.test(file) || file.includes("__tests__")) return false;
    const source = readFileSync(file, "utf8");
    return source.includes("shareImageResponse(") || /from "\.\/opengraph-image"/.test(source);
  });
}

/**
 * The route Next matches the globs against: no route groups, no `/route` file, no extension. A
 * dynamic metadata image also gets a hash suffix (`/bills/[id]/opengraph-image-1e2sri`).
 */
function routesFor(file: string): string[] {
  const route = `/${path.relative(appDir, file)}`
    .replace(/\/\([^)]+\)/g, "")
    .replace(/\/route\.tsx?$/, "")
    .replace(/\.tsx?$/, "");
  return route.includes("[") && /-image$/.test(route) ? [route, `${route}-1e2sri`] : [route];
}

function includesFor(route: string): string[] {
  return CARD_ROUTE_GLOBS.filter((glob) => picomatch(glob, { dot: true, contains: true })(route)).flatMap(
    (glob) => cardTracingIncludes()[glob] ?? []
  );
}

describe("share card assets in the function bundles (#706)", () => {
  it("finds every route that renders the card", () => {
    const routes = cardRouteFiles().flatMap(routesFor);
    expect(routes).toEqual(
      expect.arrayContaining([
        "/opengraph-image",
        "/twitter-image",
        "/bills/[id]/opengraph-image-1e2sri",
        "/bills/[id]/twitter-image-1e2sri",
        "/share/bill/[bill]/[vote]/image.png",
        "/share/bill/[bill]/[vote]/[member]/image.png",
        "/share/aggregate/[bill]/[scope]/image.png",
      ])
    );
  });

  it("traces the fonts and the logo into each of them", () => {
    for (const route of cardRouteFiles().flatMap(routesFor)) {
      expect(includesFor(route), route).toEqual(expect.arrayContaining(CARD_ASSET_FILES));
    }
  });

  it("names files that exist", () => {
    const files = filesUnder(path.join(webRoot, "assets")).map((file) => path.relative(webRoot, file));
    for (const glob of CARD_ASSET_FILES) {
      expect(files.filter((file) => picomatch(glob.replace(/^\.\//, ""))(file)), glob).not.toHaveLength(0);
    }
  });

  it("is what next.config.ts traces", async () => {
    const { default: config } = await import("../../../next.config");
    expect(config.outputFileTracingIncludes).toEqual(cardTracingIncludes());
  });
});
