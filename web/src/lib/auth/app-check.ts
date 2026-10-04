// Firebase App Check on the writes the API checks (#122, docs/design/89-aggregate-analytics.md).
// The API verifies X-Firebase-AppCheck only on the vote and import routes (mw.AppCheck in
// api/cmd/server/main.go): audit mode records whether it passed, enforce mode answers 401
// without a valid token. This module has no Firebase import, so api.ts can use it anywhere.

/** The routes (below /api/v1) that carry an App Check token, as api/cmd/server/main.go mounts them. */
const APP_CHECK_ROUTES = [/^\/bills\/[^/?]+\/vote$/, /^\/me\/votes:import$/];

export const APP_CHECK_HEADER = "X-Firebase-AppCheck";

/**
 * How long a write waits for a token before it goes without one. The first token of a session
 * runs reCAPTCHA; later ones come from the SDK's cache.
 */
export const APP_CHECK_TIMEOUT_MS = 5000;

/** Whether a call to `path` (below /api/v1, query included) should carry an App Check token. */
export function needsAppCheck(method: string | undefined, path: string): boolean {
  if ((method ?? "GET").toUpperCase() !== "POST") return false;
  const route = path.split("?")[0];
  return APP_CHECK_ROUTES.some((r) => r.test(route));
}

/** Resolves to a token, or null when there's none to send (signed out, or App Check is off). */
export type AppCheckTokenSource = () => Promise<string | null>;

// The browser's auth store registers itself here (lib/auth/provider.tsx); nothing does on the server.
let source: AppCheckTokenSource | null = null;

/** Sets where App Check tokens come from, or clears it with null. */
export function setAppCheckSource(next: AppCheckTokenSource | null): void {
  source = next;
}

/**
 * The App Check header for a write, or no header when there's no token. A write never fails over
 * App Check here: without the header, the API records the vote as not passing (audit) or refuses
 * it (enforce), and the caller shows that error like any other.
 */
export async function appCheckHeaders(timeoutMs: number = APP_CHECK_TIMEOUT_MS): Promise<Record<string, string>> {
  if (!source) return {};
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<"timeout">((resolve) => {
    timer = setTimeout(() => resolve("timeout"), timeoutMs);
  });
  try {
    const token = await Promise.race([source(), timeout]);
    if (token === "timeout") {
      console.warn(`No App Check token after ${timeoutMs} ms; sending the request without one.`);
      return {};
    }
    return token ? { [APP_CHECK_HEADER]: token } : {};
  } catch (err) {
    console.warn("No App Check token; sending the request without one:", err);
    return {};
  } finally {
    clearTimeout(timer);
  }
}
