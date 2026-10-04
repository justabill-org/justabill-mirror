import { describe, it, expect } from "vitest";
import { assertVercelBuildEnv, vercelBuildEnvError } from "../build-env";

describe("vercelBuildEnvError", () => {
  it("ignores builds outside Vercel", () => {
    expect(vercelBuildEnvError({})).toBeNull();
    expect(
      vercelBuildEnvError({ NEXT_PUBLIC_API_URL: "http://localhost:8080" }),
    ).toBeNull();
  });

  it("accepts an https origin on Vercel", () => {
    expect(
      vercelBuildEnvError({
        VERCEL: "1",
        NEXT_PUBLIC_API_URL: "https://api.example.org",
      }),
    ).toBeNull();
  });

  it.each([
    ["missing", undefined, /must be set/],
    ["empty", "", /must be set/],
    ["not a URL", "api.example.org", /not a valid URL/],
    ["http", "http://api.example.org", /https/],
    ["localhost", "http://localhost:8080", /https/],
    ["trailing slash", "https://api.example.org/", /no path/],
    ["with a path", "https://api.example.org/api/v1", /no path/],
  ])("rejects a %s API URL on Vercel", (_, value, message) => {
    expect(
      vercelBuildEnvError({ VERCEL: "1", NEXT_PUBLIC_API_URL: value }),
    ).toMatch(message);
  });
});

describe("sign-in config on Vercel (#137)", () => {
  const base = { VERCEL: "1", NEXT_PUBLIC_API_URL: "https://api.example.org" };
  const firebase = {
    NEXT_PUBLIC_FIREBASE_API_KEY: "browser-key",
    NEXT_PUBLIC_FIREBASE_AUTH_DOMAIN: "justabill.io",
    NEXT_PUBLIC_FIREBASE_PROJECT_ID: "example-prod",
  };

  it("accepts accounts on with a complete Firebase config, and accounts off without one", () => {
    expect(vercelBuildEnvError({ ...base, ...firebase, NEXT_PUBLIC_ACCOUNTS_ENABLED: "true" })).toBeNull();
    expect(vercelBuildEnvError({ ...base, NEXT_PUBLIC_ACCOUNTS_ENABLED: "false" })).toBeNull();
    expect(vercelBuildEnvError(base)).toBeNull();
  });

  it("rejects accounts on without a Firebase config", () => {
    const partial = { ...firebase, NEXT_PUBLIC_FIREBASE_PROJECT_ID: "" };
    expect(vercelBuildEnvError({ ...base, ...partial, NEXT_PUBLIC_ACCOUNTS_ENABLED: "1" })).toMatch(
      /NEXT_PUBLIC_FIREBASE_PROJECT_ID/,
    );
  });

  it("rejects the Auth emulator", () => {
    expect(
      vercelBuildEnvError({ ...base, ...firebase, NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST: "localhost:9099" }),
    ).toMatch(/emulator/);
  });

  it("rejects an App Check debug token in production builds only (#122)", () => {
    const debug = { ...base, ...firebase, NEXT_PUBLIC_FIREBASE_APPCHECK_DEBUG_TOKEN: "debug-token" };
    expect(vercelBuildEnvError({ ...debug, VERCEL_ENV: "production" })).toMatch(/APPCHECK_DEBUG_TOKEN/);
    expect(vercelBuildEnvError({ ...debug, VERCEL_ENV: "preview" })).toBeNull();
  });
});

describe("sign-in providers on Vercel (#652)", () => {
  const accounts = {
    VERCEL: "1",
    NEXT_PUBLIC_API_URL: "https://api.example.org",
    NEXT_PUBLIC_ACCOUNTS_ENABLED: "true",
    NEXT_PUBLIC_FIREBASE_API_KEY: "browser-key",
    NEXT_PUBLIC_FIREBASE_AUTH_DOMAIN: "justabill.io",
    NEXT_PUBLIC_FIREBASE_PROJECT_ID: "example-prod",
  };

  it("accepts known providers, and an unset list (all three)", () => {
    expect(vercelBuildEnvError({ ...accounts, NEXT_PUBLIC_AUTH_PROVIDERS: "google.com" })).toBeNull();
    expect(vercelBuildEnvError({ ...accounts, NEXT_PUBLIC_AUTH_PROVIDERS: "google.com, apple.com" })).toBeNull();
    expect(vercelBuildEnvError(accounts)).toBeNull();
  });

  it("rejects an unknown provider ID, with accounts on or off", () => {
    expect(vercelBuildEnvError({ ...accounts, NEXT_PUBLIC_AUTH_PROVIDERS: "google.com,microsoft" })).toMatch(
      /unknown sign-in providers: microsoft\b/,
    );
    expect(
      vercelBuildEnvError({ ...accounts, NEXT_PUBLIC_ACCOUNTS_ENABLED: "false", NEXT_PUBLIC_AUTH_PROVIDERS: "google" }),
    ).toMatch(/unknown sign-in providers: google\b/);
  });

  it("rejects an empty list only with accounts on", () => {
    expect(vercelBuildEnvError({ ...accounts, NEXT_PUBLIC_AUTH_PROVIDERS: " , " })).toMatch(
      /NEXT_PUBLIC_AUTH_PROVIDERS must name at least one/,
    );
    expect(
      vercelBuildEnvError({ ...accounts, NEXT_PUBLIC_ACCOUNTS_ENABLED: "false", NEXT_PUBLIC_AUTH_PROVIDERS: "" }),
    ).toBeNull();
  });
});

describe("assertVercelBuildEnv", () => {
  it("throws only when the env is invalid", () => {
    expect(() => assertVercelBuildEnv({ VERCEL: "1" })).toThrow(
      /NEXT_PUBLIC_API_URL/,
    );
    expect(() =>
      assertVercelBuildEnv({
        VERCEL: "1",
        NEXT_PUBLIC_API_URL: "https://api.example.org",
      }),
    ).not.toThrow();
  });
});
