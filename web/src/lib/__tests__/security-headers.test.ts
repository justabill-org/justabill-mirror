import { describe, it, expect } from "vitest";
import { contentSecurityPolicy, securityHeaders } from "../security-headers";

// Parses "a x y; b z" into { a: ["x", "y"], b: ["z"] }.
function directives(csp: string): Record<string, string[]> {
  return Object.fromEntries(
    csp.split("; ").map((d) => {
      const [name, ...values] = d.split(" ");
      return [name, values];
    }),
  );
}

function header(headers: { key: string; value: string }[], key: string) {
  return headers.find((h) => h.key === key)?.value;
}

const production = {
  NODE_ENV: "production",
  VERCEL: "1",
  VERCEL_ENV: "production",
  NEXT_PUBLIC_API_URL: "https://api.justabill.io",
};
const preview = {
  ...production,
  VERCEL_ENV: "preview",
  NEXT_PUBLIC_API_URL: "https://api-preview.justabill.io",
};
const development = { NODE_ENV: "development" };

// Sign-in (#137) in a production build: accounts on and a complete Firebase config.
const signIn = {
  ...production,
  NEXT_PUBLIC_ACCOUNTS_ENABLED: "true",
  NEXT_PUBLIC_FIREBASE_API_KEY: "browser-key",
  NEXT_PUBLIC_FIREBASE_AUTH_DOMAIN: "justabill.io",
  NEXT_PUBLIC_FIREBASE_PROJECT_ID: "example-prod",
};

describe("contentSecurityPolicy with sign-in on (#137)", () => {
  it("allows gapi, the auth helper frame on our domain and the Identity Platform APIs", () => {
    const csp = directives(contentSecurityPolicy(signIn));
    expect(csp["script-src"]).toEqual(["'self'", "'unsafe-inline'", "https://apis.google.com"]);
    expect(csp["frame-src"]).toEqual(["https://justabill.io"]);
    expect(csp["connect-src"]).toEqual([
      "'self'",
      "https://api.justabill.io",
      "https://identitytoolkit.googleapis.com",
      "https://securetoken.googleapis.com",
    ]);
    expect(csp["frame-ancestors"]).toEqual(["'none'"]);
  });

  it("allows the Auth emulator instead of Google's APIs when one is set", () => {
    const csp = directives(
      contentSecurityPolicy({ ...signIn, NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST: "localhost:9099" }),
    );
    expect(csp["connect-src"]).toEqual(["'self'", "https://api.justabill.io", "http://localhost:9099"]);
    expect(csp["frame-src"]).toEqual(["http://localhost:9099"]);
  });

  it("adds nothing when accounts are off or the Firebase config is incomplete", () => {
    const off = directives(contentSecurityPolicy({ ...signIn, NEXT_PUBLIC_ACCOUNTS_ENABLED: "false" }));
    const partial = directives(contentSecurityPolicy({ ...signIn, NEXT_PUBLIC_FIREBASE_API_KEY: "" }));
    for (const csp of [off, partial]) {
      expect(csp["script-src"]).toEqual(["'self'", "'unsafe-inline'"]);
      expect(csp["frame-src"]).toEqual(["'none'"]);
      expect(csp["connect-src"]).toEqual(["'self'", "https://api.justabill.io"]);
    }
  });
});

describe("contentSecurityPolicy with App Check (#122)", () => {
  const appCheck = {
    ...signIn,
    NEXT_PUBLIC_FIREBASE_APP_ID: "1:123:web:abc",
    NEXT_PUBLIC_RECAPTCHA_SITE_KEY: "site-key",
  };

  it("adds reCAPTCHA's script and frame and App Check's token exchange", () => {
    const csp = directives(contentSecurityPolicy(appCheck));
    expect(csp["script-src"]).toEqual([
      "'self'",
      "'unsafe-inline'",
      "https://apis.google.com",
      "https://www.google.com/recaptcha/",
      "https://www.gstatic.com/recaptcha/",
    ]);
    expect(csp["frame-src"]).toEqual([
      "https://justabill.io",
      "https://www.google.com/recaptcha/",
      "https://recaptcha.google.com/recaptcha/",
    ]);
    expect(csp["connect-src"]).toEqual([
      "'self'",
      "https://api.justabill.io",
      "https://identitytoolkit.googleapis.com",
      "https://securetoken.googleapis.com",
      "https://content-firebaseappcheck.googleapis.com",
    ]);
  });

  it("keeps the emulator next to App Check's sources", () => {
    const csp = directives(
      contentSecurityPolicy({ ...appCheck, NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST: "localhost:9099" }),
    );
    expect(csp["connect-src"]).toEqual([
      "'self'",
      "https://api.justabill.io",
      "http://localhost:9099",
      "https://content-firebaseappcheck.googleapis.com",
    ]);
    expect(csp["frame-src"]?.[0]).toBe("http://localhost:9099");
  });

  it("adds nothing without the site key, or with accounts off", () => {
    const noKey = directives(contentSecurityPolicy({ ...appCheck, NEXT_PUBLIC_RECAPTCHA_SITE_KEY: "" }));
    expect(noKey["script-src"]).toEqual(["'self'", "'unsafe-inline'", "https://apis.google.com"]);
    const off = directives(contentSecurityPolicy({ ...appCheck, NEXT_PUBLIC_ACCOUNTS_ENABLED: "false" }));
    expect(off["script-src"]).toEqual(["'self'", "'unsafe-inline'"]);
    expect(off["frame-src"]).toEqual(["'none'"]);
  });
});

describe("contentSecurityPolicy", () => {
  it("matches the design's policy in production", () => {
    expect(contentSecurityPolicy(production)).toBe(
      "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; " +
        "img-src 'self' data: blob:; font-src 'self'; connect-src 'self' https://api.justabill.io; " +
        "frame-src 'none'; frame-ancestors 'none'; object-src 'none'; base-uri 'self'; " +
        "form-action 'self'; upgrade-insecure-requests",
    );
  });

  it("allows the preview build's own API origin", () => {
    const csp = directives(contentSecurityPolicy(preview));
    expect(csp["connect-src"]).toEqual(["'self'", "https://api-preview.justabill.io"]);
    expect(csp["script-src"]).not.toContain("'unsafe-eval'");
    expect(csp).toHaveProperty("upgrade-insecure-requests");
  });

  it("allows eval and the local API in development", () => {
    const csp = directives(contentSecurityPolicy(development));
    expect(csp["script-src"]).toEqual(["'self'", "'unsafe-inline'", "'unsafe-eval'"]);
    expect(csp["connect-src"]).toEqual(["'self'", "http://localhost:8080"]);
    expect(csp).not.toHaveProperty("upgrade-insecure-requests");
  });

  it("does not upgrade requests on a plain-http production run", () => {
    const csp = directives(
      contentSecurityPolicy({ NODE_ENV: "production", NEXT_PUBLIC_API_URL: "http://localhost:8080" }),
    );
    expect(csp["script-src"]).not.toContain("'unsafe-eval'");
    expect(csp).not.toHaveProperty("upgrade-insecure-requests");
  });

  it.each([
    ["a path", "https://api.justabill.io/api/v1", "https://api.justabill.io"],
    ["a trailing slash", "https://api.justabill.io/", "https://api.justabill.io"],
    ["a port", "http://localhost:9090", "http://localhost:9090"],
    ["an invalid URL", "api.justabill.io", "http://localhost:8080"],
    ["an empty value", "", "http://localhost:8080"],
  ])("reduces an API URL with %s to an origin", (_, url, origin) => {
    const csp = directives(contentSecurityPolicy({ NODE_ENV: "production", NEXT_PUBLIC_API_URL: url }));
    expect(csp["connect-src"]).toEqual(["'self'", origin]);
  });

  it("blocks framing, plugins and foreign form targets", () => {
    for (const env of [production, preview, development]) {
      const csp = directives(contentSecurityPolicy(env));
      expect(csp["frame-ancestors"]).toEqual(["'none'"]);
      expect(csp["frame-src"]).toEqual(["'none'"]);
      expect(csp["object-src"]).toEqual(["'none'"]);
      expect(csp["base-uri"]).toEqual(["'self'"]);
      expect(csp["form-action"]).toEqual(["'self'"]);
    }
  });
});

describe("securityHeaders", () => {
  it("sends every header in production", () => {
    const headers = securityHeaders(production);
    expect(Object.fromEntries(headers.map((h) => [h.key, h.value]))).toEqual({
      "Content-Security-Policy": contentSecurityPolicy(production),
      "Strict-Transport-Security": "max-age=63072000; includeSubDomains",
      "X-Frame-Options": "DENY",
      "X-Content-Type-Options": "nosniff",
      "Referrer-Policy": "strict-origin-when-cross-origin",
      "Permissions-Policy":
        "camera=(), microphone=(), geolocation=(self), payment=(), usb=(), browsing-topics=()",
      "Cross-Origin-Opener-Policy": "same-origin-allow-popups",
    });
  });

  it("sends HSTS on previews without preload", () => {
    const hsts = header(securityHeaders(preview), "Strict-Transport-Security");
    expect(hsts).toBe("max-age=63072000; includeSubDomains");
    expect(hsts).not.toContain("preload");
  });

  it("leaves HSTS out in development", () => {
    const headers = securityHeaders(development);
    expect(header(headers, "Strict-Transport-Security")).toBeUndefined();
    expect(header(headers, "X-Frame-Options")).toBe("DENY");
  });

  it("uses unique header names", () => {
    const keys = securityHeaders(production).map((h) => h.key.toLowerCase());
    expect(new Set(keys).size).toBe(keys.length);
  });
});
