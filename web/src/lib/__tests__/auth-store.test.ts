import { describe, expect, it, vi } from "vitest";
import { ApiError } from "../api";
import { firebaseConfig, signInConfig } from "../auth/config";
import { prefersRedirect, providerParameters, safeNext, signInErrorMessage } from "../auth/providers";
import { firebaseAuthRewrites } from "../auth/rewrites";
import { browserSessionHint, type HintEnv } from "../auth/session-hint";
import { createAuthStore, type AccountApi, type AuthBackend, type TokenUser } from "../auth/store";
import type { User } from "../types";

// The auth store (#137) behind useUser(): it follows the Firebase session through a fake backend
// and makes sure the API account exists. The API is faked too.

const account: User = { id: "u-1", state: "CA", district: 12, created_at: "2026-09-29T00:00:00Z" };
const noAccount = () => new ApiError(403, "Forbidden", '{"error":"no account","code":"no_account"}');

function fakeUser(uid = "firebase-uid-1", token = "id-token-1"): TokenUser {
  return { uid, getIdToken: vi.fn(async () => token) };
}

function fakeBackend() {
  let listener: ((user: TokenUser | null) => void) | null = null;
  let current: TokenUser | null = null;
  const backend = {
    onIdTokenChanged: vi.fn((l: (user: TokenUser | null) => void) => {
      listener = l;
      return () => {
        listener = null;
      };
    }),
    currentUser: () => current,
    signIn: vi.fn<AuthBackend["signIn"]>(async () => {}),
    reauthenticate: vi.fn<AuthBackend["reauthenticate"]>(async () => {}),
    completeRedirect: vi.fn<AuthBackend["completeRedirect"]>(async () => {}),
    signOut: vi.fn(async () => {
      current = null;
      listener?.(null);
    }),
  } satisfies AuthBackend;
  return {
    backend,
    /** Firebase reports a session change, as onIdTokenChanged does. */
    emit(user: TokenUser | null) {
      current = user;
      listener?.(user);
    },
  };
}

/** A browser's localStorage, sessionStorage and window events, in memory; `signedIn` presets the note. */
function fakeBrowser(signedIn = false) {
  const memory = () => {
    const items = new Map<string, string>();
    return {
      items,
      getItem: (k: string) => items.get(k) ?? null,
      setItem: (k: string, v: string) => void items.set(k, v),
      removeItem: (k: string) => void items.delete(k),
    };
  };
  const local = memory();
  const session = memory();
  if (signedIn) local.setItem("jab.signed-in.v1", "1");
  const events = new EventTarget();
  const env: HintEnv = { local, session, events };
  return {
    local,
    session,
    hint: browserSessionHint(() => env),
    /** Another tab writes `key`, as the browser reports it with a storage event. */
    otherTabWrites(key: string, newValue: string | null) {
      events.dispatchEvent(Object.assign(new Event("storage"), { key, newValue }));
    },
  };
}

// Most tests start where someone has signed in on this device, so start() follows Firebase.
function setup(
  api: AccountApi = { getMe: vi.fn(async () => account), createMe: vi.fn(async () => account) },
  ua = "Desktop",
  extra: Partial<AuthBackend> = {},
  browser = fakeBrowser(true)
) {
  const fake = fakeBackend();
  const backend: AuthBackend = { ...fake.backend, ...extra };
  const load = vi.fn(async () => backend);
  const store = createAuthStore(load, api, () => ua, browser.hint);
  return { ...fake, store, api, load, browser };
}

// Lets the store's promise chains settle.
const flush = () => new Promise((r) => setTimeout(r, 0));

describe("App Check in the auth store (#122)", () => {
  function withAppCheck() {
    const token = vi.fn(async () => "app-check-token");
    const startAppCheck = vi.fn(() => token);
    return { token, startAppCheck, ...setup(undefined, "Desktop", { startAppCheck }) };
  }

  it("starts App Check only once someone is signed in, and only once", async () => {
    const { store, emit, startAppCheck } = withAppCheck();
    store.start();
    await flush();
    emit(null);
    expect(startAppCheck).not.toHaveBeenCalled();
    await expect(store.getAppCheckToken()).resolves.toBeNull();

    emit(fakeUser());
    await flush();
    emit(fakeUser("firebase-uid-2"));
    await flush();
    expect(startAppCheck).toHaveBeenCalledTimes(1);
    await expect(store.getAppCheckToken()).resolves.toBe("app-check-token");
  });

  it("gives no token after sign-out", async () => {
    const { store, emit, token } = withAppCheck();
    store.start();
    await flush();
    emit(fakeUser());
    await flush();
    await store.signOut();
    await expect(store.getAppCheckToken()).resolves.toBeNull();
    expect(token).not.toHaveBeenCalled();
  });

  it("gives no token when the backend has no App Check", async () => {
    const { store, emit } = setup(undefined, "Desktop", { startAppCheck: () => null });
    store.start();
    await flush();
    emit(fakeUser());
    await flush();
    expect(store.getState().status).toBe("signed-in");
    await expect(store.getAppCheckToken()).resolves.toBeNull();
  });

  it("passes App Check's failure on, for the caller to send the write without a token", async () => {
    const startAppCheck = () => () => Promise.reject(new Error("reCAPTCHA blocked"));
    const { store, emit } = setup(undefined, "Desktop", { startAppCheck });
    store.start();
    await flush();
    emit(fakeUser());
    await flush();
    await expect(store.getAppCheckToken()).rejects.toThrow("reCAPTCHA blocked");
  });
});

describe("createAuthStore", () => {
  it("starts loading, then signed-out when Firebase has no session", async () => {
    const { store, emit } = setup();
    expect(store.getState().status).toBe("loading");
    store.start();
    await flush();
    emit(null);
    expect(store.getState()).toEqual({ status: "signed-out", account: null, signInError: null });
  });

  it("loads the account with the ID token and never creates one that exists", async () => {
    const { store, emit, api } = setup();
    store.start();
    await flush();
    emit(fakeUser());
    await flush();
    expect(api.getMe).toHaveBeenCalledWith("id-token-1");
    expect(api.createMe).not.toHaveBeenCalled();
    expect(store.getState()).toMatchObject({ status: "signed-in", account });
  });

  it("creates the account with POST /me only when GET /me answers no_account", async () => {
    const api = { getMe: vi.fn(async () => Promise.reject(noAccount())), createMe: vi.fn(async () => account) };
    const { store, emit } = setup(api);
    store.start();
    await flush();
    emit(fakeUser());
    await flush();
    expect(api.createMe).toHaveBeenCalledWith("id-token-1");
    expect(store.getState().status).toBe("signed-in");
  });

  it("goes to error on any other API failure, and retry loads it again", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const api = {
      getMe: vi.fn<(t: string) => Promise<User>>().mockRejectedValueOnce(new ApiError(503, "Unavailable", "{}")),
      createMe: vi.fn(async () => account),
    };
    api.getMe.mockResolvedValue(account);
    const { store, emit } = setup(api);
    store.start();
    await flush();
    emit(fakeUser());
    await flush();
    expect(store.getState().status).toBe("error");
    expect(api.createMe).not.toHaveBeenCalled();

    store.retry();
    await flush();
    expect(store.getState()).toMatchObject({ status: "signed-in", account });
    vi.mocked(console.error).mockRestore();
  });

  it("ignores hourly token refreshes for the same user", async () => {
    const { store, emit, api } = setup();
    store.start();
    await flush();
    const user = fakeUser();
    emit(user);
    await flush();
    emit(user);
    await flush();
    expect(api.getMe).toHaveBeenCalledTimes(1);
  });

  it("drops a slow account load for a user who signed out meanwhile", async () => {
    let resolve: (u: User) => void = () => {};
    const api = { getMe: vi.fn(() => new Promise<User>((r) => (resolve = r))), createMe: vi.fn() };
    const { store, emit } = setup(api);
    store.start();
    await flush();
    emit(fakeUser());
    await flush();
    emit(null);
    resolve(account);
    await flush();
    expect(store.getState().status).toBe("signed-out");
  });

  it("hands out a fresh ID token for the current user, and rejects when signed out", async () => {
    const { store, emit } = setup();
    store.start();
    await flush();
    await expect(store.getIdToken()).rejects.toThrow("not signed in");
    emit(fakeUser("uid", "token-2"));
    await expect(store.getIdToken()).resolves.toBe("token-2");
  });

  it("signs in with a popup on desktop and a redirect on phones", async () => {
    const desktop = setup(undefined, "Mozilla/5.0 (X11; Linux x86_64) Chrome/140");
    await desktop.store.signIn("google.com");
    expect(desktop.backend.signIn).toHaveBeenCalledWith("google.com", "popup");

    const phone = setup(undefined, "Mozilla/5.0 (iPhone; CPU iPhone OS 19_0) Mobile/15E148");
    await phone.store.signIn("apple.com");
    expect(phone.backend.signIn).toHaveBeenCalledWith("apple.com", "redirect");
  });

  it("falls back to a redirect when the popup is blocked", async () => {
    const { store, backend } = setup();
    backend.signIn.mockRejectedValueOnce({ code: "auth/popup-blocked" });
    await store.signIn("microsoft.com");
    expect(backend.signIn.mock.calls).toEqual([
      ["microsoft.com", "popup"],
      ["microsoft.com", "redirect"],
    ]);
    expect(store.getState().signInError).toBeNull();
  });

  it("shows a sign-in error, but not for a popup the user closed", async () => {
    const { store, backend } = setup();
    backend.signIn.mockRejectedValueOnce({ code: "auth/account-exists-with-different-credential" });
    await store.signIn("google.com");
    expect(store.getState().signInError).toMatch(/another provider/);

    backend.signIn.mockRejectedValueOnce({ code: "auth/popup-closed-by-user" });
    await store.signIn("google.com");
    expect(store.getState().signInError).toBeNull();
  });

  it("reports a failed redirect sign-in when it comes back", async () => {
    const { store, backend } = setup();
    backend.completeRedirect.mockRejectedValueOnce({ code: "auth/network-request-failed" });
    store.start();
    await flush();
    expect(store.getState().signInError).toMatch(/couldn't reach/);
  });

  it("says so when the SDK can't load, and tries again on the next sign-in", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const fake = fakeBackend();
    const load = vi.fn<() => Promise<AuthBackend>>().mockRejectedValueOnce(new Error("chunk failed"));
    load.mockResolvedValue(fake.backend);
    const store = createAuthStore(load, { getMe: vi.fn(), createMe: vi.fn() }, () => "Desktop");

    await store.signIn("google.com");
    expect(store.getState().signInError).toMatch(/couldn't reach/);
    await store.signIn("google.com");
    expect(fake.backend.signIn).toHaveBeenCalledWith("google.com", "popup");
    expect(store.getState().signInError).toBeNull();
    vi.mocked(console.error).mockRestore();
  });

  it("stops following the session when stopped", async () => {
    const { store, backend } = setup();
    const stop = store.start();
    stop();
    await flush();
    expect(backend.onIdTokenChanged).not.toHaveBeenCalled();
  });

  it("replaces the account after a save, only while signed in", async () => {
    const { store, emit } = setup();
    const moved = { ...account, state: "IL", district: 13 };
    store.setAccount(moved);
    expect(store.getState().account).toBeNull();
    store.start();
    await flush();
    emit(fakeUser());
    await flush();
    store.setAccount(moved);
    expect(store.getState().account).toEqual(moved);
  });

  it("signs in again for a fresh sign-in time, with the same popup and redirect rules", async () => {
    const desktop = setup();
    desktop.backend.reauthenticate.mockRejectedValueOnce({ code: "auth/popup-blocked" });
    await desktop.store.reauthenticate();
    expect(desktop.backend.reauthenticate.mock.calls).toEqual([["popup"], ["redirect"]]);

    const phone = setup(undefined, "Mozilla/5.0 (Linux; Android 16) Mobile");
    await phone.store.reauthenticate();
    expect(phone.backend.reauthenticate).toHaveBeenCalledWith("redirect");
  });

  it("rejects a failed re-sign-in with Firebase's error, leaving the sign-in message alone", async () => {
    const { store, backend } = setup();
    backend.reauthenticate.mockRejectedValueOnce({ code: "auth/user-mismatch" });
    await expect(store.reauthenticate()).rejects.toEqual({ code: "auth/user-mismatch" });
    expect(store.getState().signInError).toBeNull();
    expect(signInErrorMessage("auth/user-mismatch")).toMatch(/different account/);
  });

  it("signs out through Firebase", async () => {
    const { store, emit, backend } = setup();
    store.start();
    await flush();
    emit(fakeUser());
    await flush();
    await store.signOut();
    expect(backend.signOut).toHaveBeenCalled();
    expect(store.getState().status).toBe("signed-out");
  });
});

describe("loading Firebase only for visitors who sign in (#757)", () => {
  const phone = "Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) Mobile/15E148";

  it("doesn't load Firebase for a visitor nobody has signed in for, and shows them signed out", () => {
    const { store, load } = setup(undefined, phone, {}, fakeBrowser(false));
    store.start();
    expect(load).not.toHaveBeenCalled();
    expect(store.getState()).toEqual({ status: "signed-out", account: null, signInError: null });
  });

  it("loads it at start once someone has signed in on this device", async () => {
    const { store, load, backend } = setup(undefined, phone, {}, fakeBrowser(true));
    store.start();
    await flush();
    expect(load).toHaveBeenCalledTimes(1);
    expect(backend.onIdTokenChanged).toHaveBeenCalledTimes(1);
    expect(backend.completeRedirect).toHaveBeenCalledTimes(1);
  });

  it("loads it on a popup sign-in, follows the session, and notes the sign-in until sign-out", async () => {
    const browser = fakeBrowser(false);
    const { store, load, backend, emit } = setup(undefined, "Desktop", {}, browser);
    store.start();
    await store.signIn("google.com");
    expect(load).toHaveBeenCalledTimes(1);
    expect(backend.signIn).toHaveBeenCalledWith("google.com", "popup");
    emit(fakeUser());
    await flush();
    expect(store.getState().status).toBe("signed-in");
    expect(browser.local.items.get("jab.signed-in.v1")).toBe("1");

    await store.signOut();
    expect(store.getState().status).toBe("signed-out");
    expect(browser.local.items.has("jab.signed-in.v1")).toBe(false);
  });

  it("forgets the note when Firebase finds no session, e.g. after the site's data was cleared", async () => {
    const browser = fakeBrowser(true);
    const { store, emit } = setup(undefined, "Desktop", {}, browser);
    store.start();
    await flush();
    emit(null);
    expect(browser.local.items.has("jab.signed-in.v1")).toBe(false);
  });

  it("loads it on the way back from a redirect sign-in, and only then", async () => {
    const browser = fakeBrowser(false);
    const leaving = setup(undefined, phone, {}, browser);
    leaving.store.start();
    await leaving.store.signIn("apple.com");
    expect(leaving.backend.signIn).toHaveBeenCalledWith("apple.com", "redirect");
    // Firebase reports nobody yet while the redirect leaves the page; the redirect note stays.
    leaving.emit(null);

    // The page loads again in the same tab, with a new store.
    const back = setup(undefined, phone, {}, browser);
    back.store.start();
    await flush();
    expect(back.load).toHaveBeenCalledTimes(1);
    expect(back.backend.completeRedirect).toHaveBeenCalledTimes(1);

    // A sign-in that never finished doesn't load it on the next page.
    const next = setup(undefined, phone, {}, browser);
    next.store.start();
    expect(next.load).not.toHaveBeenCalled();
  });

  it("notes a redirect after a blocked popup too", async () => {
    const browser = fakeBrowser(false);
    const { store, backend } = setup(undefined, "Desktop", {}, browser);
    backend.signIn.mockRejectedValueOnce({ code: "auth/popup-blocked" });
    await store.signIn("google.com");
    expect(browser.session.items.get("jab.sign-in-redirect.v1")).toBe("1");
  });

  it("finishes a redirect sign-in when development runs start, stop and start again", async () => {
    const browser = fakeBrowser(false);
    browser.session.setItem("jab.sign-in-redirect.v1", "1");
    const { store, backend } = setup(undefined, phone, {}, browser);
    store.start()();
    store.start();
    await flush();
    expect(backend.onIdTokenChanged).toHaveBeenCalledTimes(1);
  });

  it("follows a sign-in in another tab", async () => {
    const browser = fakeBrowser(false);
    const { store, load, emit } = setup(undefined, "Desktop", {}, browser);
    store.start();
    browser.otherTabWrites("jab.votes.v1", "{}");
    browser.otherTabWrites("jab.signed-in.v1", null);
    expect(load).not.toHaveBeenCalled();

    browser.otherTabWrites("jab.signed-in.v1", "1");
    await flush();
    emit(fakeUser());
    await flush();
    expect(store.getState().status).toBe("signed-in");
  });

  it("stops listening for other tabs when stopped", () => {
    const browser = fakeBrowser(false);
    const { store, load } = setup(undefined, "Desktop", {}, browser);
    store.start()();
    browser.otherTabWrites("jab.signed-in.v1", "1");
    expect(load).not.toHaveBeenCalled();
  });

  it("reads blocked storage as nobody signed in, without throwing", () => {
    const blocked = {
      getItem: () => {
        throw new DOMException("blocked", "SecurityError");
      },
      setItem: () => {
        throw new DOMException("blocked", "SecurityError");
      },
      removeItem: () => {
        throw new DOMException("blocked", "SecurityError");
      },
    };
    const hint = browserSessionHint(() => ({ local: blocked, session: blocked, events: null }));
    expect(hint.signedIn()).toBe(false);
    expect(() => hint.setSignedIn(true)).not.toThrow();
    expect(() => hint.markRedirect()).not.toThrow();
    expect(hint.takeRedirect()).toBe(false);
    expect(() => hint.onSignIn(() => {})()).not.toThrow();
  });

  it("is empty during a server render", () => {
    const hint = browserSessionHint();
    expect(hint.signedIn()).toBe(false);
    expect(hint.takeRedirect()).toBe(false);
  });
});

describe("sign-in helpers", () => {
  it("asks Microsoft for personal accounts only, and no extra scopes anywhere", () => {
    expect(providerParameters("microsoft.com")).toEqual({ prompt: "select_account", tenant: "consumers" });
    expect(providerParameters("google.com")).toEqual({ prompt: "select_account" });
    expect(providerParameters("apple.com")).toEqual({});
  });

  it.each([
    ["Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) Mobile/15E148", true],
    ["Mozilla/5.0 (Linux; Android 16; Pixel 10) Chrome/140 Mobile Safari/537.36", true],
    ["Mozilla/5.0 (Macintosh; Intel Mac OS X 15_6) Safari/605.1.15", false],
  ])("prefers a redirect for %s: %s", (ua, want) => {
    expect(prefersRedirect(ua)).toBe(want);
  });

  it.each([
    [null, "/vote"],
    ["/settings", "/settings"],
    ["/bills/hr-119-1?x=1", "/bills/hr-119-1?x=1"],
    ["https://evil.example", "/vote"],
    ["//evil.example", "/vote"],
    ["/\\evil.example", "/vote"],
    ["settings", "/vote"],
    // URL parsing strips tab, CR and LF, so each of these resolves to //evil.example (#756).
    ["/\t/evil.example", "/vote"],
    ["/\n/evil.example", "/vote"],
    ["/\r//evil.example", "/vote"],
    [new URLSearchParams("next=/%09/evil.example").get("next"), "/vote"],
    ["/\t\\evil.example", "/vote"],
    ["/bills/hr-119-1?tab=text#votes", "/bills/hr-119-1?tab=text#votes"],
    ["/bills?q=a%20b", "/bills?q=a%20b"],
  ])("safeNext(%j) is %j", (next, want) => {
    expect(safeNext(next)).toBe(want);
  });

  it("has a message for unknown errors and none for cancellations", () => {
    expect(signInErrorMessage("auth/user-cancelled")).toBeNull();
    expect(signInErrorMessage("auth/something-new")).toMatch(/try again/);
  });
});

describe("config", () => {
  const env = {
    NEXT_PUBLIC_FIREBASE_API_KEY: "k",
    NEXT_PUBLIC_FIREBASE_AUTH_DOMAIN: "justabill.io",
    NEXT_PUBLIC_FIREBASE_PROJECT_ID: "example-prod",
  };

  it("needs the API key, auth domain and project ID; the emulator host is optional", () => {
    expect(firebaseConfig(env)).toEqual({ apiKey: "k", authDomain: "justabill.io", projectId: "example-prod" });
    expect(firebaseConfig({ ...env, NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST: "localhost:9099" })).toMatchObject({
      emulatorHost: "localhost:9099",
    });
    expect(firebaseConfig({ ...env, NEXT_PUBLIC_FIREBASE_API_KEY: " " })).toBeNull();
  });

  it("adds App Check only with both the app ID and the reCAPTCHA site key (#122)", () => {
    const appCheck = { ...env, NEXT_PUBLIC_FIREBASE_APP_ID: " 1:123:web:abc ", NEXT_PUBLIC_RECAPTCHA_SITE_KEY: "site" };
    expect(firebaseConfig(appCheck)?.appCheck).toEqual({ appId: "1:123:web:abc", siteKey: "site" });
    expect(firebaseConfig({ ...appCheck, NEXT_PUBLIC_FIREBASE_APPCHECK_DEBUG_TOKEN: "dbg" })?.appCheck).toEqual({
      appId: "1:123:web:abc",
      siteKey: "site",
      debugToken: "dbg",
    });
    expect(firebaseConfig({ ...appCheck, NEXT_PUBLIC_RECAPTCHA_SITE_KEY: "" })).not.toHaveProperty("appCheck");
    expect(firebaseConfig({ ...appCheck, NEXT_PUBLIC_FIREBASE_APP_ID: undefined })).not.toHaveProperty("appCheck");
  });

  it("turns sign-in on only with accounts on", () => {
    const config = firebaseConfig(env);
    expect(signInConfig(true, config)).toBe(config);
    expect(signInConfig(false, config)).toBeNull();
    expect(signInConfig(true, null)).toBeNull();
  });

  it("proxies /__/auth/* to the project's firebaseapp.com with the path unchanged", () => {
    expect(firebaseAuthRewrites("example-prod")).toEqual([
      { source: "/__/auth/:path*", destination: "https://example-prod.firebaseapp.com/__/auth/:path*" },
    ]);
    expect(firebaseAuthRewrites(undefined)).toEqual([]);
  });
});
