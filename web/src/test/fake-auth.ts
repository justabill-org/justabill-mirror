import { vi } from "vitest";
import { act } from "@testing-library/react";
import type { AuthState, AuthStatus, AuthStore } from "@/lib/auth/store";
import type { User } from "@/lib/types";

export const fakeAccount: User = { id: "u-1", state: "CA", district: 12, created_at: "2026-09-29T00:00:00Z" };

/** An auth store whose state the test sets (`set`, inside act); its actions are spies. */
export function fakeAuthStore(status: AuthStatus, extra: Partial<AuthState> = {}) {
  let state: AuthState = {
    status,
    account: status === "signed-in" ? fakeAccount : null,
    signInError: null,
    ...extra,
  };
  const listeners = new Set<() => void>();
  const stop = vi.fn();
  const store = {
    getState: () => state,
    subscribe(l: () => void) {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    start: vi.fn(() => stop),
    getIdToken: vi.fn(async () => "id-token-1"),
    signIn: vi.fn(async () => {}),
    reauthenticate: vi.fn(async () => {}),
    signOut: vi.fn(async () => {}),
    retry: vi.fn(),
    setAccount: vi.fn(),
    getAppCheckToken: vi.fn(async () => null),
  } satisfies AuthStore;
  return {
    store,
    stop,
    set(next: Partial<AuthState>) {
      state = { ...state, ...next };
      act(() => listeners.forEach((l) => l()));
    },
  };
}
