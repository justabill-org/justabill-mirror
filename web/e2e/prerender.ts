import { readFile } from "node:fs/promises";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { COOKIE_NAME, experimentForPath } from "../src/lib/experiments/registry";

// `next build` prerenders the ISR pages (/vote, revalidate = 300) before the e2e API is up, so
// they hold their "unavailable" state until the revalidate window passes, far longer than a run.
// Tag revalidation can't reach them either: the build's fetches failed, so the pages carry no tags.

/** The build's prerender manifest (`next build` writes it), of which only the preview ID is read. */
const MANIFEST = path.resolve(__dirname, "../.next/prerender-manifest.json");

/** The API's rate limit window: a fixed minute per IP (api/internal/middleware/ratelimit.go). */
const RATE_LIMIT_WINDOW_MS = 60_000;

/**
 * Renders a prerendered page again from the API, as on-demand revalidation does: `next start`
 * regenerates the page in place when the request carries the build's preview ID in the
 * `x-prerender-revalidate` header. Every later visit gets the new page.
 *
 * While an experiment runs on the page, the proxy would put the visitor in either arm, so the
 * page's context is put in control with the arm's cookie: the page regenerated is the one later
 * visits see. (Regenerating both arms instead doubles the page's API calls, and the API's 60 a
 * minute per IP then fails the run with 429s.)
 *
 * The specs before it can still use up that minute, and the render then fails with a 500 (the
 * server's API calls come from its own address, so the test can't ask the API about them). So a 500
 * is tried once more after a full window, when the server's calls have their minute again, and a
 * second 500 fails as before.
 */
export async function regenerate(page: Page, pathname: string): Promise<void> {
  const manifest = JSON.parse(await readFile(MANIFEST, "utf8")) as { preview: { previewModeId: string } };
  const experiment = experimentForPath(pathname);
  if (experiment) {
    await page.context().addCookies([
      { name: COOKIE_NAME, value: `${experiment.id}.control`, domain: "localhost", path: "/" },
    ]);
  }
  const headers = { "x-prerender-revalidate": manifest.preview.previewModeId };
  let res = await page.request.get(pathname, { headers });
  if (res.status() === 500) {
    test.info().setTimeout(test.info().timeout + RATE_LIMIT_WINDOW_MS);
    await new Promise((r) => setTimeout(r, RATE_LIMIT_WINDOW_MS));
    res = await page.request.get(pathname, { headers });
  }
  expect(res.status(), `regenerate ${pathname}`).toBe(200);
}
