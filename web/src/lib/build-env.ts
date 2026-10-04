// Build-time checks for Vercel deployments, run from next.config.ts.
//
// NEXT_PUBLIC_API_URL is inlined into the bundle at build time, and api.ts
// falls back to http://localhost:8080 when it is unset. On Vercel that would
// ship a site that calls localhost, so the build fails instead. The same goes
// for sign-in (#137): accounts on without a Firebase config would ship a
// Sign in link that can't work, and the Auth emulator is for local runs only. An App Check debug
// token is inlined into the bundle too, where anyone could read it and pass App Check with it, so
// production builds refuse one (#122). NEXT_PUBLIC_AUTH_PROVIDERS picks the sign-in buttons
// (#652): a misspelled ID would silently drop one, and an empty list with accounts on would ship a
// sign-in page with no way in.

import { firebaseConfig } from "./auth/config";
import { enabledProviders, PROVIDERS, unknownProviderIds } from "./auth/providers";

type BuildEnv = Record<string, string | undefined>;

function signInEnvError(env: BuildEnv): string | null {
  if (env.NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST?.trim()) {
    return (
      "NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST must not be set for Vercel builds: " +
      "the Auth emulator is for local runs."
    );
  }
  if (env.VERCEL_ENV === "production" && env.NEXT_PUBLIC_FIREBASE_APPCHECK_DEBUG_TOKEN?.trim()) {
    return (
      "NEXT_PUBLIC_FIREBASE_APPCHECK_DEBUG_TOKEN must not be set for production builds: " +
      "it would let anyone pass App Check. Use it only in Preview and local runs."
    );
  }
  const unknown = unknownProviderIds(env.NEXT_PUBLIC_AUTH_PROVIDERS);
  if (unknown.length > 0) {
    return (
      `NEXT_PUBLIC_AUTH_PROVIDERS lists unknown sign-in providers: ${unknown.join(", ")}. ` +
      `Use a comma-separated list of ${PROVIDERS.map((p) => p.id).join(", ")}.`
    );
  }
  const flag = env.NEXT_PUBLIC_ACCOUNTS_ENABLED?.trim().toLowerCase();
  if (flag !== "true" && flag !== "1") return null;
  if (!firebaseConfig(env)) {
    return (
      "NEXT_PUBLIC_ACCOUNTS_ENABLED is on, so NEXT_PUBLIC_FIREBASE_API_KEY, NEXT_PUBLIC_FIREBASE_AUTH_DOMAIN " +
      "and NEXT_PUBLIC_FIREBASE_PROJECT_ID must be set for Vercel builds."
    );
  }
  if (enabledProviders(env.NEXT_PUBLIC_AUTH_PROVIDERS).length === 0) {
    return "NEXT_PUBLIC_ACCOUNTS_ENABLED is on, so NEXT_PUBLIC_AUTH_PROVIDERS must name at least one sign-in provider.";
  }
  return null;
}

export function vercelBuildEnvError(env: BuildEnv): string | null {
  if (!env.VERCEL) return null;

  const raw = env.NEXT_PUBLIC_API_URL;
  if (!raw) {
    return "NEXT_PUBLIC_API_URL must be set for Vercel builds (Project Settings → Environment Variables).";
  }

  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return `NEXT_PUBLIC_API_URL is not a valid URL: ${JSON.stringify(raw)}`;
  }
  if (url.protocol !== "https:") {
    return `NEXT_PUBLIC_API_URL must use https:// on Vercel, got ${JSON.stringify(raw)}`;
  }
  // api.ts appends "/api/v1/..." and "/health" to the value as-is.
  if (raw !== url.origin) {
    return `NEXT_PUBLIC_API_URL must be an origin with no path or trailing slash (e.g. ${url.origin}), got ${JSON.stringify(raw)}`;
  }
  return signInEnvError(env);
}

export function assertVercelBuildEnv(env: BuildEnv): void {
  const error = vercelBuildEnvError(env);
  if (error) throw new Error(error);
}
