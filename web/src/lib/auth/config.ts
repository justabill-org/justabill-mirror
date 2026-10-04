import { accountsEnabled } from "../accounts";

/** The public Firebase web config. None of these values are secret (#55, "Secrets"). */
export interface FirebaseWebConfig {
  apiKey: string;
  /** Our own domain in production, so redirect sign-in stays first-party (the /__/auth rewrite). */
  authDomain: string;
  projectId: string;
  /** host:port of the Firebase Auth emulator, for local development and tests only. */
  emulatorHost?: string;
  /** Firebase App Check for the vote and import calls (#122); absent when it isn't configured. */
  appCheck?: AppCheckConfig;
}

/**
 * Firebase App Check with the reCAPTCHA Enterprise provider (docs/design/89-aggregate-analytics.md).
 * It needs the Firebase web app's ID, which App Check's token exchange is keyed on, and a
 * reCAPTCHA key for the site.
 */
export interface AppCheckConfig {
  appId: string;
  siteKey: string;
  /**
   * An App Check debug token, for local development and CI against a real project: the SDK
   * exchanges it instead of running reCAPTCHA. "true" makes the SDK generate one and log it, to
   * register in the Firebase console. Vercel production builds refuse it (build-env.ts).
   */
  debugToken?: string;
}

type AuthEnv = Record<string, string | undefined>;

// NEXT_PUBLIC_* values are inlined at build time only where they're referenced literally, so the
// browser bundle has to read them one by one rather than from an env object.
function publicEnv(): AuthEnv {
  return {
    NEXT_PUBLIC_FIREBASE_API_KEY: process.env.NEXT_PUBLIC_FIREBASE_API_KEY,
    NEXT_PUBLIC_FIREBASE_AUTH_DOMAIN: process.env.NEXT_PUBLIC_FIREBASE_AUTH_DOMAIN,
    NEXT_PUBLIC_FIREBASE_PROJECT_ID: process.env.NEXT_PUBLIC_FIREBASE_PROJECT_ID,
    NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST: process.env.NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST,
    NEXT_PUBLIC_FIREBASE_APP_ID: process.env.NEXT_PUBLIC_FIREBASE_APP_ID,
    NEXT_PUBLIC_RECAPTCHA_SITE_KEY: process.env.NEXT_PUBLIC_RECAPTCHA_SITE_KEY,
    NEXT_PUBLIC_FIREBASE_APPCHECK_DEBUG_TOKEN: process.env.NEXT_PUBLIC_FIREBASE_APPCHECK_DEBUG_TOKEN,
  };
}

/** The App Check config, or null unless both the app ID and the reCAPTCHA site key are set. */
export function appCheckConfig(env: AuthEnv = publicEnv()): AppCheckConfig | null {
  const appId = env.NEXT_PUBLIC_FIREBASE_APP_ID?.trim();
  const siteKey = env.NEXT_PUBLIC_RECAPTCHA_SITE_KEY?.trim();
  const debugToken = env.NEXT_PUBLIC_FIREBASE_APPCHECK_DEBUG_TOKEN?.trim();
  if (!appId || !siteKey) return null;
  return { appId, siteKey, ...(debugToken ? { debugToken } : {}) };
}

/** The Firebase config from the NEXT_PUBLIC_FIREBASE_* variables, or null when one is missing. */
export function firebaseConfig(env: AuthEnv = publicEnv()): FirebaseWebConfig | null {
  const apiKey = env.NEXT_PUBLIC_FIREBASE_API_KEY?.trim();
  const authDomain = env.NEXT_PUBLIC_FIREBASE_AUTH_DOMAIN?.trim();
  const projectId = env.NEXT_PUBLIC_FIREBASE_PROJECT_ID?.trim();
  const emulatorHost = env.NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST?.trim();
  if (!apiKey || !authDomain || !projectId) return null;
  const appCheck = appCheckConfig(env);
  return {
    apiKey,
    authDomain,
    projectId,
    ...(emulatorHost ? { emulatorHost } : {}),
    ...(appCheck ? { appCheck } : {}),
  };
}

/**
 * The Firebase config when sign-in is on in this build: accounts are on (lib/accounts.ts) and the
 * config is complete. Otherwise null, and the browser never downloads the Firebase SDK.
 */
export function signInConfig(
  accounts: boolean = accountsEnabled(),
  config: FirebaseWebConfig | null = firebaseConfig()
): FirebaseWebConfig | null {
  return accounts ? config : null;
}
