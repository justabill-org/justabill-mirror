import { ApiError } from "@/lib/api";
import { setOnboarded } from "./onboarding";

/** What deleting needs from useUser(). */
export interface DeletingUser {
  getIdToken(): Promise<string>;
  reauthenticate(): Promise<void>;
  signOut(): Promise<void>;
}

function needsRecentLogin(err: unknown): boolean {
  return err instanceof ApiError && err.status === 401 && err.code === "requires_recent_login";
}

/**
 * Deletes the signed-in account through DELETE /me (votes, district and followed bills, then the
 * sign-in itself), and signs out. The API wants a sign-in from the last five minutes, so when it
 * answers requires_recent_login the user signs in again and it's tried once more. Rejects with
 * the API's or Firebase's error; a redirect sign-in leaves the page instead.
 */
export async function deleteAccount(user: DeletingUser, deleteMe: (token: string) => Promise<void>): Promise<void> {
  try {
    await deleteMe(await user.getIdToken());
  } catch (err) {
    if (!needsRecentLogin(err)) throw err;
    await user.reauthenticate();
    await deleteMe(await user.getIdToken());
  }
  setOnboarded(null);
  await user.signOut();
}
