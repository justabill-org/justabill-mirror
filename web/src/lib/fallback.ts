// Honest failures (#73): pages show the API's data or an error page, never example data in
// production. `next dev` falls back to the fixtures in lib/examples, so the UI can be worked on
// without the API running. So do Vercel previews until go-public (#678): the API is private
// (ACCESS_MODE=private) and previews get no server key, so every API call from a preview fails, and
// without fixtures a reviewer couldn't check a page's layout there. `next build`, `next start`,
// production and tests run with another NODE_ENV and VERCEL_ENV, so they never see fixtures.

import { ApiError } from "./api";

type Env = Record<string, string | undefined>;

/**
 * Whether this is a Vercel preview that shows example data when the API fails, with the banner
 * that says so (#678). PREVIEW_EXAMPLE_DATA=off turns it off: set it on the Preview environment
 * once the API opens to previews at go-public, then remove this code.
 */
export function previewExamplesOn(env: Env = process.env): boolean {
  return env.VERCEL_ENV === "preview" && env.PREVIEW_EXAMPLE_DATA?.trim().toLowerCase() !== "off";
}

/** Whether pages may show fixtures when the API fails: under `next dev`, and on previews (#678). */
export function fixturesAllowed(env: Env = process.env): boolean {
  return env.NODE_ENV === "development" || previewExamplesOn(env);
}

/**
 * Whether this render is `next build` prerendering a page, when the API isn't reachable. A
 * prerendered page that can't load its data renders an "unavailable" state instead of failing
 * the build; at request time the same failure throws, so Next.js keeps the last good page.
 */
export function isBuildPhase(env: Env = process.env): boolean {
  return env.NEXT_PHASE === "phase-production-build";
}

/** Whether the API said the thing doesn't exist (404) or can't exist (400, a malformed ID). */
export function isNotFound(err: unknown): boolean {
  return err instanceof ApiError && (err.status === 404 || err.status === 400);
}

/**
 * Whether a failed request falls back to the fixture. Under `next dev` a not-found is rethrown: a
 * bad bill ID is a 404 in development too, not the example bill. On a preview a 404 falls back,
 * since the private API answers every request it refuses with a bare 404; a 400 (a malformed ID)
 * is still the 404 page.
 */
function fallsBack(err: unknown, env: Env): boolean {
  if (previewExamplesOn(env)) return !(err instanceof ApiError && err.status === 400);
  return fixturesAllowed(env) && !isNotFound(err);
}

/**
 * Resolves to the request's result. If it fails, rethrows so the nearest error.tsx renders,
 * except under `next dev` and on Vercel previews (#678), where it resolves to the fixture instead
 * (see fallsBack for which failures). Production never falls back.
 */
export async function orDevFixture<T>(request: Promise<T>, fixture: T, env: Env = process.env): Promise<T> {
  try {
    return await request;
  } catch (err) {
    if (!fallsBack(err, env)) throw err;
    console.warn("API request failed; showing example data (next dev and previews only):", err);
    return fixture;
  }
}
