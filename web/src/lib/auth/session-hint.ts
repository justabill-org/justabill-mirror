// Notes in this browser whether anyone has signed in here, so the auth store loads Firebase only
// for them (#757). Firebase Auth's initialization loads Google's sign-in helper (gapi, from
// apis.google.com) on phones and Safari even with nobody signed in, and the Privacy page promises
// that a visitor who doesn't sign in loads no third-party scripts.

import type { StorageLike } from "@/lib/local/storage";

/** Set (localStorage) once Firebase reports a signed-in user here; cleared when it reports none. */
export const SIGNED_IN_KEY = "jab.signed-in.v1"; // gitleaks:allow (a storage key name)
/** Set (sessionStorage) just before a redirect sign-in leaves the page, read once on the way back. */
export const REDIRECT_KEY = "jab.sign-in-redirect.v1";

export interface SessionHint {
  /** Whether someone signed in on this device and Firebase hasn't reported them gone since. */
  signedIn(): boolean;
  setSignedIn(on: boolean): void;
  /** Notes a redirect sign-in about to leave the page. */
  markRedirect(): void;
  /** Whether this tab is coming back from a redirect sign-in; clears the note. */
  takeRedirect(): boolean;
  /** Calls `listener` when another tab notes a sign-in. Returns an unsubscribe function. */
  onSignIn(listener: () => void): () => void;
}

/** What the hint needs from the browser; each part is null where it's missing or blocked. */
export interface HintEnv {
  local: StorageLike | null;
  session: StorageLike | null;
  events: Pick<EventTarget, "addEventListener" | "removeEventListener"> | null;
}

function storage(get: () => Storage): StorageLike | null {
  try {
    return get();
  } catch {
    return null; // Blocked storage throws on access (Safari with cookies off, sandboxed frames).
  }
}

function browserHintEnv(): HintEnv {
  if (typeof window === "undefined") return { local: null, session: null, events: null };
  return { local: storage(() => window.localStorage), session: storage(() => window.sessionStorage), events: window };
}

/** Runs a storage call, treating a throw (quota, blocked storage) as a missing value. */
function attempt<T>(run: () => T, fallback: T): T {
  try {
    return run();
  } catch {
    return fallback;
  }
}

/**
 * The hint on the browser's storage. Where storage is blocked it reads as nobody signed in, so the
 * page stays free of third-party scripts until the visitor presses sign-in.
 */
export function browserSessionHint(env: () => HintEnv = browserHintEnv): SessionHint {
  return {
    signedIn: () => attempt(() => env().local?.getItem(SIGNED_IN_KEY) === "1", false),
    setSignedIn(on) {
      const { local } = env();
      attempt(() => (on ? local?.setItem(SIGNED_IN_KEY, "1") : local?.removeItem(SIGNED_IN_KEY)), undefined);
    },
    markRedirect() {
      const { session } = env();
      attempt(() => session?.setItem(REDIRECT_KEY, "1"), undefined);
    },
    takeRedirect() {
      const { session } = env();
      return attempt(() => {
        const pending = session?.getItem(REDIRECT_KEY) === "1";
        session?.removeItem(REDIRECT_KEY);
        return pending;
      }, false);
    },
    onSignIn(listener) {
      const { events } = env();
      const onStorage = (e: Event) => {
        const { key, newValue } = e as StorageEvent;
        if (key === SIGNED_IN_KEY && newValue === "1") listener();
      };
      events?.addEventListener("storage", onStorage);
      return () => events?.removeEventListener("storage", onStorage);
    },
  };
}
