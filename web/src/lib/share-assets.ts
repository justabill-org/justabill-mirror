// What the share card image reads from disk at request time (#88, #706), and the routes that
// render it. A file missing from a route's function bundle breaks that route's link previews on
// Vercel, while `next start` (which reads the source tree) keeps working. Turbopack already traces
// the literal paths in image.tsx; next.config.ts includes them by name as well, so a refactor that
// hides a path from the tracer can't drop it. Plain data, so next.config.ts can import it without
// next/og.

/** Geist Regular and Bold (`loadFonts()`) and the mascot (`loadLogo()`), from the web/ root. */
export const CARD_ASSET_FILES = ["./assets/fonts/*.ttf", "./assets/logo.png"];

/**
 * Route globs (picomatch, against routes like `/bills/[id]/opengraph-image`) for every route that
 * renders the card: the /share/** images, the site's default Open Graph and X images, and each
 * bill page's. share-assets.test.ts fails if a card route under src/app isn't matched.
 */
export const CARD_ROUTE_GLOBS = [
  "/share/**",
  "/opengraph-image*",
  "/twitter-image*",
  // Next matches with picomatch's `contains`, so the two above match these too; named anyway, so a
  // reader (or a change in that matching) doesn't miss the bill pages. Dynamic metadata images get
  // a hash suffix in their route (`/bills/[id]/opengraph-image-1e2sri`), hence the `*`.
  "/bills/*/opengraph-image*",
  "/bills/*/twitter-image*",
];

/** next.config.ts's outputFileTracingIncludes for the card routes. */
export function cardTracingIncludes(): Record<string, string[]> {
  return Object.fromEntries(CARD_ROUTE_GLOBS.map((glob) => [glob, CARD_ASSET_FILES]));
}
