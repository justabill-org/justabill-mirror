import { ApiError } from "@/lib/api";
import type { User } from "@/lib/types";
import { prefersRedirect, signInErrorMessage, type ProviderId } from "./providers";
import { browserSessionHint, type SessionHint } from "./session-hint";

/**
 * - disabled: sign-in is off in this build (accounts are off, or there's no Firebase config).
 * - loading: we don't know yet, or the account is being loaded after sign-in.
 * - signed-out: nobody is signed in.
 * - signed-in: signed in, and `account` exists on the API.
 * - error: signed in with the provider, but the API account couldn't be loaded or created.
 */
export type AuthStatus = "disabled" | "loading" | "signed-out" | "signed-in" | "error";

export interface AuthState {
  status: AuthStatus;
  account: User | null;
  /** A message for the sign-in page, e.g. from a failed redirect sign-in. */
  signInError: string | null;
}

/** The part of a Firebase user the store needs. */
export interface TokenUser {
  uid: string;
  getIdToken(forceRefresh?: boolean): Promise<string>;
}

export type SignInMode = "popup" | "redirect";

/** What the store needs from Firebase Auth; firebase.ts implements it, tests fake it. */
export interface AuthBackend {
  onIdTokenChanged(listener: (user: TokenUser | null) => void): () => void;
  currentUser(): TokenUser | null;
  signIn(provider: ProviderId, mode: SignInMode): Promise<void>;
  /** Signs the current user in again with the provider they used, for a fresh auth_time. */
  reauthenticate(mode: SignInMode): Promise<void>;
  /** Settles a pending redirect sign-in; rejects with its error. */
  completeRedirect(): Promise<void>;
  signOut(): Promise<void>;
  /**
   * Starts Firebase App Check, which loads reCAPTCHA, and returns its token getter; the same one
   * on every call. Absent, or null, when App Check isn't configured (#122).
   */
  startAppCheck?(): (() => Promise<string>) | null;
}

export interface AccountApi {
  getMe(token: string): Promise<User>;
  createMe(token: string): Promise<User>;
}

export interface AuthStore {
  getState(): AuthState;
  subscribe(listener: () => void): () => void;
  /**
   * Starts following the Firebase session: it loads the backend only when someone has signed in on
   * this device, a redirect sign-in is coming back, or another tab signs in (#757); otherwise the
   * visitor is signed out without loading Firebase, until they sign in. Returns a stop function.
   */
  start(): () => void;
  /** A fresh ID token for the signed-in user (Firebase refreshes it when it's near expiry). */
  getIdToken(): Promise<string>;
  signIn(provider: ProviderId): Promise<void>;
  /**
   * Asks the signed-in user to sign in again (the API wants a recent sign-in before deleting an
   * account). Rejects with Firebase's error, e.g. auth/popup-closed-by-user. On phones it
   * redirects, and the page loads again once they're back.
   */
  reauthenticate(): Promise<void>;
  signOut(): Promise<void>;
  /** Tries loading the account again after an error. */
  retry(): void;
  /** Replaces the loaded account after a change the API answered with (e.g. PATCH /me). */
  setAccount(account: User): void;
  /**
   * An App Check token for the vote and import calls, or null when nobody is signed in or App
   * Check is off. Rejects when App Check fails (reCAPTCHA blocked, or the exchange refused).
   */
  getAppCheckToken(): Promise<string | null>;
}

const DISABLED: AuthState = { status: "disabled", account: null, signInError: null };

/** The store used when accounts are off: it never loads Firebase and never signs anyone in. */
export const disabledAuthStore: AuthStore = {
  getState: () => DISABLED,
  subscribe: () => () => {},
  start: () => () => {},
  getIdToken: () => Promise.reject(new Error("accounts are disabled")),
  signIn: () => Promise.reject(new Error("accounts are disabled")),
  reauthenticate: () => Promise.reject(new Error("accounts are disabled")),
  signOut: () => Promise.resolve(),
  retry: () => {},
  setAccount: () => {},
  getAppCheckToken: () => Promise.resolve(null),
};

const LOADING: AuthState = { status: "loading", account: null, signInError: null };

/** The store server renders use: nobody is known there, so it stays loading until hydration. */
export const serverAuthStore: AuthStore = {
  ...disabledAuthStore,
  getState: () => LOADING,
};

export function errorCode(err: unknown): string | undefined {
  if (err && typeof err === "object" && "code" in err && typeof err.code === "string") {
    return err.code;
  }
  return undefined;
}

/**
 * Follows the Firebase session and makes sure the signed-in user has an API account: GET /me,
 * and POST /me only when the API answers 403 no_account, so page views never write. `hint` says
 * whether anyone signed in on this device, so Firebase loads only for them (#757).
 */
export function createAuthStore(
  loadBackend: () => Promise<AuthBackend>,
  api: AccountApi,
  userAgent: () => string = () => (typeof navigator === "undefined" ? "" : navigator.userAgent),
  hint: SessionHint = browserSessionHint()
): AuthStore {
  let state: AuthState = { status: "loading", account: null, signInError: null };
  const listeners = new Set<() => void>();
  let backend: AuthBackend | null = null;
  let backendPromise: Promise<AuthBackend> | null = null;
  // Set while the store follows the backend's session (from start, or from a sign-in).
  let unfollow: (() => void) | null = null;
  // Whether this page load is the way back from a redirect sign-in. Read once, at the first start:
  // reading clears the note, and React runs effects twice in development.
  let returning: boolean | undefined;
  // The uid whose account is loaded or loading, and a counter that drops stale results.
  let accountUid: string | null = null;
  let generation = 0;
  // Set once someone has signed in: reCAPTCHA loads only for signed-in users (the privacy policy).
  let appCheckToken: (() => Promise<string>) | null = null;

  function set(next: Partial<AuthState>) {
    state = { ...state, ...next };
    listeners.forEach((l) => l());
  }

  /** Runs a popup or redirect flow: a redirect on phones, and after a blocked popup. */
  async function withMode(run: (mode: SignInMode) => Promise<void>): Promise<void> {
    const redirect = () => {
      // The page loads again on the way back, and must then load Firebase to finish the sign-in.
      hint.markRedirect();
      return run("redirect");
    };
    if (prefersRedirect(userAgent())) return redirect();
    try {
      await run("popup");
    } catch (err) {
      // A blocked popup falls back to a redirect in the same tab.
      if (errorCode(err) === "auth/popup-blocked") return redirect();
      throw err;
    }
  }

  function getBackend(): Promise<AuthBackend> {
    backendPromise ??= loadBackend().then(
      (b) => {
        backend = b;
        return b;
      },
      (err: unknown) => {
        backendPromise = null; // So a later sign-in tries loading it again.
        throw err;
      }
    );
    return backendPromise;
  }

  /** Follows the backend's session, once, and settles a redirect sign-in coming back. */
  function follow(b: AuthBackend) {
    if (unfollow) return;
    unfollow = b.onIdTokenChanged(onUser);
    b.completeRedirect().catch((err: unknown) => {
      set({ signInError: signInErrorMessage(errorCode(err)) });
    });
  }

  async function loadAccount(user: TokenUser) {
    const gen = ++generation;
    accountUid = user.uid;
    set({ status: "loading", account: null });
    try {
      const token = await user.getIdToken();
      let account: User;
      try {
        account = await api.getMe(token);
      } catch (err) {
        if (!(err instanceof ApiError && err.status === 403 && err.code === "no_account")) throw err;
        account = await api.createMe(token);
      }
      if (gen !== generation) return;
      // Started at sign-in rather than at the first vote, so reCAPTCHA sees the session first.
      appCheckToken ??= backend?.startAppCheck?.() ?? null;
      set({ status: "signed-in", account, signInError: null });
    } catch (err) {
      console.error("Failed to load the account:", err);
      if (gen === generation) set({ status: "error", account: null });
    }
  }

  function onUser(user: TokenUser | null) {
    hint.setSignedIn(user !== null);
    if (!user) {
      generation++;
      accountUid = null;
      set({ status: "signed-out", account: null });
      return;
    }
    // Hourly token refreshes fire this too; the account doesn't change with them.
    if (user.uid === accountUid && state.status !== "error") return;
    void loadAccount(user);
  }

  return {
    getState: () => state,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    start() {
      let stopped = false;
      const begin = () => {
        getBackend()
          .then((b) => {
            if (!stopped) follow(b);
          })
          .catch((err: unknown) => {
            console.error("Failed to load sign-in:", err);
            set({ status: "signed-out", signInError: signInErrorMessage(undefined) });
          });
      };
      let unhint = () => {};
      returning ??= hint.takeRedirect();
      if (returning || hint.signedIn()) {
        begin();
      } else if (!unfollow) {
        // Nobody has signed in here: don't load Firebase, whose initialization loads Google's
        // sign-in helper on phones and Safari, until they do, here or in another tab.
        set({ status: "signed-out" });
        unhint = hint.onSignIn(begin);
      }
      return () => {
        stopped = true;
        unhint();
        unfollow?.();
        unfollow = null;
      };
    },
    async getIdToken() {
      const user = backend?.currentUser();
      if (!user) throw new Error("not signed in");
      return user.getIdToken();
    },
    async signIn(provider) {
      let b: AuthBackend;
      try {
        b = await getBackend();
      } catch (err) {
        // The SDK chunk didn't load (offline, or a deploy replaced it).
        console.error("Failed to load sign-in:", err);
        set({ signInError: signInErrorMessage("auth/network-request-failed") });
        return;
      }
      set({ signInError: null });
      // A visitor nobody had signed in for: start following the session now.
      follow(b);
      try {
        await withMode((mode) => b.signIn(provider, mode));
      } catch (err) {
        set({ signInError: signInErrorMessage(errorCode(err)) });
      }
    },
    async reauthenticate() {
      const b = await getBackend();
      await withMode((mode) => b.reauthenticate(mode));
    },
    async signOut() {
      await backend?.signOut();
    },
    retry() {
      const user = backend?.currentUser();
      if (user) void loadAccount(user);
    },
    setAccount(account) {
      if (state.status === "signed-in") set({ account });
    },
    async getAppCheckToken() {
      if (state.status !== "signed-in" || !appCheckToken) return null;
      return appCheckToken();
    },
  };
}
