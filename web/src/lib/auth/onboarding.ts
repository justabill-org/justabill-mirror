// Where sign-in goes next (#138): a new account, or one with votes on this device that aren't in
// it yet, goes through the short onboarding at /signup (find your district, add this device's
// votes) once per device. Everyone else goes straight back to ?next=.

import { browserEnv, type LocalEnv } from "@/lib/local/storage";
import type { User } from "@/lib/types";

/** The account that finished onboarding on this device: an opaque ID, nothing personal. */
export const ONBOARDED_KEY = "jab.onboarded";

export const ONBOARDING_PATH = "/signup";

type Env = () => LocalEnv | null;

/** Whether this account already went through onboarding on this device. */
export function onboarded(accountId: string, env: Env = browserEnv): boolean {
  try {
    return env()?.storage?.getItem(ONBOARDED_KEY) === accountId;
  } catch {
    return false;
  }
}

/** Records that the account finished onboarding here, or forgets it (null). Best effort. */
export function setOnboarded(accountId: string | null, env: Env = browserEnv): void {
  try {
    const storage = env()?.storage;
    if (accountId) storage?.setItem(ONBOARDED_KEY, accountId);
    else storage?.removeItem(ONBOARDED_KEY);
  } catch {
    // Storage full or blocked: onboarding just shows again next time.
  }
}

/**
 * The path to go to once signed in: onboarding when the account has no district or this device
 * has votes to offer, unless the account already went through it here; else `next`.
 */
export function afterSignInPath(
  account: User,
  next: string,
  localVoteCount: number,
  isOnboarded: (accountId: string) => boolean = onboarded
): string {
  const needed = !account.state || localVoteCount > 0;
  if (!needed || isOnboarded(account.id)) return next;
  return `${ONBOARDING_PATH}?next=${encodeURIComponent(next)}`;
}
