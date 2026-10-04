// Security headers for every route, applied by headers() in next.config.ts.
// Design: docs/design/51-vercel.md ("Security headers").
//
// The CSP is static: no nonce, because a nonce forces every page to render
// dynamically and would break cached bill pages (#74). The price is
// 'unsafe-inline' in script-src for Next's inline bootstrap scripts. A
// proxy.ts without a nonce keeps pages cached (Vercel runs it before the
// cache): src/proxy.ts exists only while an A/B experiment runs, and never
// sets a nonce (docs/design/580-ab-experiments.md).
//
// To allow a new source, add it to the directive below and to the tests in
// __tests__/security-headers.test.ts.

import { accountsEnabled } from "./accounts";
import { firebaseConfig } from "./auth/config";

type HeaderEnv = Record<string, string | undefined>;

export type Header = { key: string; value: string };

// Same fallback as api.ts, so local runs allow the API they actually call.
const DEFAULT_API_URL = "http://localhost:8080";

// Two years, the value Vercel uses for custom domains. No preload yet: that
// is hard to undo and waits for the maintainer (design question 6).
const HSTS = "max-age=63072000; includeSubDomains";

function apiOrigin(env: HeaderEnv): string {
  const raw = env.NEXT_PUBLIC_API_URL || DEFAULT_API_URL;
  try {
    return new URL(raw).origin;
  } catch {
    // build-env.ts rejects this on Vercel; elsewhere, fall back rather than
    // fail a local build over a header.
    return new URL(DEFAULT_API_URL).origin;
  }
}

interface SignInSources {
  script: string[];
  connect: string[];
  frame: string[];
}

/**
 * What App Check (#122) loads for signed-in users when it's configured: reCAPTCHA Enterprise's
 * script and its iframe (Google's CSP guide for reCAPTCHA), and App Check's token exchange.
 */
const APP_CHECK_SOURCES: SignInSources = {
  script: ["https://www.google.com/recaptcha/", "https://www.gstatic.com/recaptcha/"],
  connect: ["https://content-firebaseappcheck.googleapis.com"],
  frame: ["https://www.google.com/recaptcha/", "https://recaptcha.google.com/recaptcha/"],
};

/**
 * What Firebase sign-in (#137) loads, only when it's on in this build. The SDK loads Google's
 * gapi from apis.google.com, frames the auth helper page from authDomain (our own domain, proxied
 * by the /__/auth rewrite, or the emulator), and calls the Identity Toolkit and Secure Token APIs.
 * App Check's sources are added when it's configured.
 */
function signInSources(env: HeaderEnv): SignInSources {
  const config = firebaseConfig(env);
  if (!config || !accountsEnabled(env.NEXT_PUBLIC_ACCOUNTS_ENABLED, env.NODE_ENV)) {
    return { script: [], connect: [], frame: [] };
  }
  const emulator = config.emulatorHost ? `http://${config.emulatorHost}` : null;
  const auth: SignInSources = {
    script: ["https://apis.google.com"],
    connect: emulator ? [emulator] : ["https://identitytoolkit.googleapis.com", "https://securetoken.googleapis.com"],
    frame: [emulator ?? `https://${config.authDomain}`],
  };
  if (!config.appCheck) return auth;
  return {
    script: [...auth.script, ...APP_CHECK_SOURCES.script],
    connect: [...auth.connect, ...APP_CHECK_SOURCES.connect],
    frame: [...auth.frame, ...APP_CHECK_SOURCES.frame],
  };
}

export function contentSecurityPolicy(env: HeaderEnv): string {
  const dev = env.NODE_ENV === "development";
  const api = apiOrigin(env);
  const signIn = signInSources(env);

  const directives: [string, ...string[]][] = [
    ["default-src", "'self'"],
    // React's dev tooling needs eval; production builds don't.
    ["script-src", "'self'", "'unsafe-inline'", ...(dev ? ["'unsafe-eval'"] : []), ...signIn.script],
    ["style-src", "'self'", "'unsafe-inline'"],
    ["img-src", "'self'", "data:", "blob:"],
    ["font-src", "'self'"],
    ["connect-src", "'self'", api, ...signIn.connect],
    signIn.frame.length > 0 ? ["frame-src", ...signIn.frame] : ["frame-src", "'none'"],
    ["frame-ancestors", "'none'"],
    ["object-src", "'none'"],
    ["base-uri", "'self'"],
    ["form-action", "'self'"],
  ];
  // Only when the site is served over https (the API is https on every
  // Vercel build). On a plain-http next start or compose run it would rewrite
  // same-origin requests to https and break the page.
  if (!dev && api.startsWith("https://")) {
    directives.push(["upgrade-insecure-requests"]);
  }
  return directives.map((d) => d.join(" ")).join("; ");
}

export function securityHeaders(env: HeaderEnv): Header[] {
  const headers: Header[] = [
    { key: "Content-Security-Policy", value: contentSecurityPolicy(env) },
    // For old browsers that ignore frame-ancestors.
    { key: "X-Frame-Options", value: "DENY" },
    { key: "X-Content-Type-Options", value: "nosniff" },
    { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
    {
      key: "Permissions-Policy",
      // Location for this site only, for "Use my location" on /scorecard (#668); never for frames.
      value: "camera=(), microphone=(), geolocation=(self), payment=(), usb=(), browsing-topics=()",
    },
    // Not same-origin: popup sign-in (#137) needs a handle on its popup.
    { key: "Cross-Origin-Opener-Policy", value: "same-origin-allow-popups" },
  ];
  // Browsers ignore HSTS over http, but next dev --experimental-https would
  // pin it on localhost for two years.
  if (env.NODE_ENV !== "development") {
    headers.push({ key: "Strict-Transport-Security", value: HSTS });
  }
  return headers;
}
