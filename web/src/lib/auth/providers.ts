/** The sign-in providers from the design (#55): Google, Apple and Microsoft, nothing else. */
export type ProviderId = "google.com" | "apple.com" | "microsoft.com";

export interface Provider {
  id: ProviderId;
  label: string;
}

export const PROVIDERS: Provider[] = [
  { id: "google.com", label: "Google" },
  { id: "apple.com", label: "Apple" },
  { id: "microsoft.com", label: "Microsoft" },
];

export function isProviderId(id: string): id is ProviderId {
  return PROVIDERS.some((p) => p.id === id);
}

/** The IDs in NEXT_PUBLIC_AUTH_PROVIDERS, a comma-separated list, trimmed and without blanks. */
function listedIds(raw: string): string[] {
  return raw
    .split(",")
    .map((id) => id.trim())
    .filter((id) => id !== "");
}

/** The IDs in NEXT_PUBLIC_AUTH_PROVIDERS that aren't providers, for the build check (build-env.ts). */
export function unknownProviderIds(raw: string | undefined): string[] {
  return raw === undefined ? [] : listedIds(raw).filter((id) => !isProviderId(id));
}

/**
 * The providers turned on in this build (#652), from NEXT_PUBLIC_AUTH_PROVIDERS, in its order.
 * Unset, it's all three, for development against the Auth emulator; production lists only the
 * providers Identity Platform has turned on (Google alone at launch, #133). Unknown IDs are
 * dropped here, and fail Vercel builds (build-env.ts). Inlined at build time, so a change on
 * Vercel takes a redeploy.
 */
export function enabledProviders(raw: string | undefined = process.env.NEXT_PUBLIC_AUTH_PROVIDERS): Provider[] {
  if (raw === undefined) return PROVIDERS;
  const ids = new Set(listedIds(raw));
  return [...ids].flatMap((id) => PROVIDERS.filter((p) => p.id === id));
}

/** The providers' names for a sentence: "Google", "Google or Apple", "Google, Apple or Microsoft". */
export function providerNames(providers: Provider[] = enabledProviders()): string {
  const labels = providers.map((p) => p.label);
  if (labels.length <= 1) return labels.join("");
  return `${labels.slice(0, -1).join(", ")} or ${labels.at(-1)}`;
}

/**
 * Custom OAuth parameters per provider. We ask for no scopes beyond the defaults: the app stores
 * no email or name. Microsoft is limited to personal accounts, which is what the Entra app is
 * registered for.
 */
export function providerParameters(id: ProviderId): Record<string, string> {
  switch (id) {
    case "google.com":
      return { prompt: "select_account" };
    case "microsoft.com":
      return { prompt: "select_account", tenant: "consumers" };
    case "apple.com":
      return {};
  }
}

/** Phones and tablets use redirect sign-in: popups are unreliable there. */
export function prefersRedirect(userAgent: string): boolean {
  return /Android|iPhone|iPad|iPod|Mobile/i.test(userAgent);
}

/** A user-facing message for a Firebase sign-in error code, or null to show nothing. */
export function signInErrorMessage(code: string | undefined): string | null {
  switch (code) {
    case "auth/popup-closed-by-user":
    case "auth/cancelled-popup-request":
    case "auth/user-cancelled":
      return null;
    case "auth/account-exists-with-different-credential":
      return "You already have an account with this email through another provider. Sign in with that one.";
    case "auth/operation-not-allowed":
      return "Sign-in with this provider isn't available yet. Try another one.";
    case "auth/network-request-failed":
      return "We couldn't reach the sign-in service. Check your connection and try again.";
    case "auth/user-mismatch":
      return "That's a different account. Sign in with the account you're deleting.";
    case "auth/too-many-requests":
      return "Too many sign-in attempts. Wait a few minutes and try again.";
    default:
      return "Sign-in failed. Please try again.";
  }
}

const NEXT_BASE = "https://next.invalid";

/**
 * The path to return to after sign-in: only same-site paths, so ?next= can't redirect elsewhere.
 * It resolves next the way the router will, since URL parsing drops tab, CR and LF and reads "\" as
 * "/" ("/\t/evil.example" becomes "//evil.example"), and returns the parsed path, query and hash.
 */
export function safeNext(next: string | null | undefined, fallback = "/vote"): string {
  if (!next?.startsWith("/")) return fallback;
  let url: URL;
  try {
    url = new URL(next, NEXT_BASE);
  } catch {
    return fallback;
  }
  if (url.origin !== NEXT_BASE) return fallback;
  return url.pathname + url.search + url.hash;
}
