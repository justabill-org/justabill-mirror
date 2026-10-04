"use client";

import { createContext, useContext, useEffect, useSyncExternalStore } from "react";
import { createMe, getMe } from "@/lib/api";
import type { User } from "@/lib/types";
import { setAppCheckSource } from "./app-check";
import { signInConfig } from "./config";
import type { ProviderId } from "./providers";
import {
  createAuthStore,
  disabledAuthStore,
  serverAuthStore,
  type AuthState,
  type AuthStore,
} from "./store";

// One store per browser tab, shared by every page, so the Firebase session is followed once.
let browserStore: AuthStore | undefined;

function defaultStore(): AuthStore {
  const config = signInConfig();
  if (!config) return disabledAuthStore;
  if (typeof window === "undefined") return serverAuthStore;
  browserStore ??= createAuthStore(
    // A dynamic import, so pages download the Firebase SDK only when sign-in is on.
    () => import("./firebase").then((m) => m.createFirebaseBackend(config)),
    { getMe, createMe }
  );
  // api.ts asks it for App Check tokens on the vote and import calls (#122).
  setAppCheckSource(browserStore.getAppCheckToken);
  return browserStore;
}

const AuthContext = createContext<AuthStore>(disabledAuthStore);

/**
 * Follows the Firebase session for its children. Pages stay public and cacheable: the server
 * render knows no user, and the browser fills it in after hydration (#74). `store` is for tests.
 */
export function AuthProvider({ children, store }: { children: React.ReactNode; store?: AuthStore }) {
  const value = store ?? defaultStore();
  useEffect(() => value.start(), [value]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export interface UseUser extends AuthState {
  /** A current ID token for API calls; rejects when nobody is signed in. */
  getIdToken(): Promise<string>;
  signIn(provider: ProviderId): Promise<void>;
  reauthenticate(): Promise<void>;
  signOut(): Promise<void>;
  retry(): void;
  setAccount(account: User): void;
}

/** The signed-in user's state and actions. Outside an AuthProvider, sign-in is off. */
export function useUser(): UseUser {
  const store = useContext(AuthContext);
  // Hydration must see what the server rendered: "loading" when sign-in is on, else "disabled".
  const serverState = store === disabledAuthStore ? store.getState : serverAuthStore.getState;
  const state = useSyncExternalStore(store.subscribe, store.getState, serverState);
  return {
    ...state,
    getIdToken: store.getIdToken,
    signIn: store.signIn,
    reauthenticate: store.reauthenticate,
    signOut: store.signOut,
    retry: store.retry,
    setAccount: store.setAccount,
  };
}
