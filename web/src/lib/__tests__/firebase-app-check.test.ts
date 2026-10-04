import { afterEach, describe, expect, it, vi } from "vitest";

// The Firebase backend's App Check (#122), with the Firebase SDK mocked: which provider and
// options it initializes, the web app ID, the debug token, and that it starts only once.

const appCheckSdk = vi.hoisted(() => ({
  initializeAppCheck: vi.fn<(app: unknown, options: object) => object>(() => ({ kind: "app-check" })),
  getToken: vi.fn(async () => ({ token: "app-check-token" })),
  ReCaptchaEnterpriseProvider: vi.fn(function (this: { siteKey: string }, siteKey: string) {
    this.siteKey = siteKey;
  }),
}));
const appSdk = vi.hoisted(() => ({
  getApps: vi.fn(() => []),
  initializeApp: vi.fn((options: object) => ({ options })),
}));

vi.mock("firebase/app-check", () => appCheckSdk);
vi.mock("firebase/app", () => appSdk);
vi.mock("firebase/auth", () => ({
  getAuth: () => ({ emulatorConfig: null, currentUser: null }),
  connectAuthEmulator: vi.fn(),
  getRedirectResult: vi.fn(),
  GoogleAuthProvider: vi.fn(),
  OAuthProvider: vi.fn(),
  onIdTokenChanged: vi.fn(),
  signInWithPopup: vi.fn(),
  signInWithRedirect: vi.fn(),
  signOut: vi.fn(),
}));

const { createFirebaseBackend } = await import("../auth/firebase");

const base = { apiKey: "k", authDomain: "justabill.io", projectId: "example-prod" };
const appCheck = { appId: "1:123:web:abc", siteKey: "site-key" };
const globals = globalThis as { FIREBASE_APPCHECK_DEBUG_TOKEN?: string | boolean };

afterEach(() => {
  vi.clearAllMocks();
  delete globals.FIREBASE_APPCHECK_DEBUG_TOKEN;
});

describe("createFirebaseBackend's App Check", () => {
  it("is off without an App Check config", () => {
    const backend = createFirebaseBackend(base);
    expect(backend.startAppCheck?.()).toBeNull();
    expect(appSdk.initializeApp).toHaveBeenCalledWith(base);
  });

  it("initializes reCAPTCHA Enterprise once, without background refresh, and returns its tokens", async () => {
    const backend = createFirebaseBackend({ ...base, appCheck });
    expect(appSdk.initializeApp).toHaveBeenCalledWith({ ...base, appId: "1:123:web:abc" });
    const tokens = backend.startAppCheck?.();
    expect(backend.startAppCheck?.()).toBe(tokens);
    await expect(tokens?.()).resolves.toBe("app-check-token");
    await expect(tokens?.()).resolves.toBe("app-check-token");

    expect(appCheckSdk.initializeAppCheck).toHaveBeenCalledOnce();
    expect(appCheckSdk.ReCaptchaEnterpriseProvider).toHaveBeenCalledWith("site-key");
    expect(appCheckSdk.initializeAppCheck.mock.calls[0][1]).toMatchObject({ isTokenAutoRefreshEnabled: false });
    expect(appCheckSdk.getToken).toHaveBeenCalledTimes(2);
    expect(globals.FIREBASE_APPCHECK_DEBUG_TOKEN).toBeUndefined();
  });

  it.each([
    ["debug-token-1", "debug-token-1"],
    ["true", true],
  ])("sets the debug token %j before initializing", async (debugToken, want) => {
    appCheckSdk.initializeAppCheck.mockImplementationOnce(() => {
      expect(globals.FIREBASE_APPCHECK_DEBUG_TOKEN).toBe(want);
      return { kind: "app-check" };
    });
    const backend = createFirebaseBackend({ ...base, appCheck: { ...appCheck, debugToken } });
    await backend.startAppCheck?.()?.();
    expect(appCheckSdk.initializeAppCheck).toHaveBeenCalledOnce();
  });
});
