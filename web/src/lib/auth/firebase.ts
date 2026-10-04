import { getApps, initializeApp, type FirebaseApp } from "firebase/app";
import {
  connectAuthEmulator,
  getAuth,
  getRedirectResult,
  GoogleAuthProvider,
  OAuthProvider,
  onIdTokenChanged,
  reauthenticateWithPopup,
  reauthenticateWithRedirect,
  signInWithPopup,
  signInWithRedirect,
  signOut,
  type AuthProvider,
} from "firebase/auth";
import type { AppCheckConfig, FirebaseWebConfig } from "./config";
import { isProviderId, providerParameters, type ProviderId } from "./providers";
import type { AuthBackend } from "./store";

function provider(id: ProviderId): AuthProvider {
  const p = id === "google.com" ? new GoogleAuthProvider() : new OAuthProvider(id);
  p.setCustomParameters(providerParameters(id));
  return p;
}

/**
 * Initializes App Check with the reCAPTCHA Enterprise provider and returns a token getter. The
 * App Check SDK is its own dynamic import, so only signed-in users download it and load
 * reCAPTCHA. Tokens aren't refreshed in the background: getToken mints one on the first write and
 * reuses it (the SDK keeps it in IndexedDB) until it's near expiry, so an idle tab costs no
 * reCAPTCHA assessments.
 */
function appCheckTokens(app: FirebaseApp, config: AppCheckConfig): () => Promise<string> {
  const ready = import("firebase/app-check").then((m) => {
    if (config.debugToken) {
      // Read by initializeAppCheck: the SDK then exchanges this token instead of running reCAPTCHA.
      const global = globalThis as { FIREBASE_APPCHECK_DEBUG_TOKEN?: string | boolean };
      global.FIREBASE_APPCHECK_DEBUG_TOKEN = config.debugToken === "true" ? true : config.debugToken;
    }
    const appCheck = m.initializeAppCheck(app, {
      provider: new m.ReCaptchaEnterpriseProvider(config.siteKey),
      isTokenAutoRefreshEnabled: false,
    });
    return async () => (await m.getToken(appCheck)).token;
  });
  ready.catch((err: unknown) => console.warn("Failed to load App Check:", err));
  return async () => (await ready)();
}

/**
 * The Firebase Auth backend for the auth store. It's loaded with a dynamic import, so pages only
 * download the Firebase SDK when accounts are on.
 */
export function createFirebaseBackend(config: FirebaseWebConfig): AuthBackend {
  const app =
    getApps()[0] ??
    initializeApp({
      apiKey: config.apiKey,
      authDomain: config.authDomain,
      projectId: config.projectId,
      // App Check's token exchange is keyed on the web app's ID.
      ...(config.appCheck ? { appId: config.appCheck.appId } : {}),
    });
  const auth = getAuth(app);
  if (config.emulatorHost && !auth.emulatorConfig) {
    connectAuthEmulator(auth, `http://${config.emulatorHost}`, { disableWarnings: true });
  }
  const appCheckConfig = config.appCheck;
  let appCheck: (() => Promise<string>) | null = null;

  return {
    onIdTokenChanged: (listener) => onIdTokenChanged(auth, listener),
    currentUser: () => auth.currentUser,
    async signIn(id, mode) {
      if (mode === "popup") {
        await signInWithPopup(auth, provider(id));
      } else {
        await signInWithRedirect(auth, provider(id));
      }
    },
    async reauthenticate(mode) {
      const user = auth.currentUser;
      if (!user) throw new Error("not signed in");
      // The provider they signed in with; Firebase refuses a different account (auth/user-mismatch).
      const id = user.providerData.map((p) => p.providerId).find(isProviderId);
      if (!id) throw new Error("no supported sign-in provider on this account");
      if (mode === "popup") {
        await reauthenticateWithPopup(user, provider(id));
      } else {
        await reauthenticateWithRedirect(user, provider(id));
      }
    },
    async completeRedirect() {
      await getRedirectResult(auth);
    },
    signOut: () => signOut(auth),
    startAppCheck() {
      if (!appCheckConfig) return null;
      appCheck ??= appCheckTokens(app, appCheckConfig);
      return appCheck;
    },
  };
}
