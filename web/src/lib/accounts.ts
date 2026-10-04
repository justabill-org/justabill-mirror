// The web half of the accounts switch (#72, #215). NEXT_PUBLIC_ACCOUNTS_ENABLED is inlined into
// the bundle at build time, so flipping it on Vercel takes a redeploy. Production gets it from
// #28's `accounts_enabled` OpenTofu variable, which also sets the API's ACCOUNTS_ENABLED. Like the API: on by default in development, off by default in production builds.

const ON = new Set(["true", "1"]);
const OFF = new Set(["false", "0"]);

/**
 * Whether sign-in and settings exist. When it's false, those routes render the not-found page, the
 * navbar has no account links, and the browser never loads the Firebase SDK (lib/auth).
 */
export function accountsEnabled(
  flag: string | undefined = process.env.NEXT_PUBLIC_ACCOUNTS_ENABLED,
  nodeEnv: string | undefined = process.env.NODE_ENV
): boolean {
  const v = flag?.trim().toLowerCase() ?? "";
  if (ON.has(v)) return true;
  if (OFF.has(v)) return false;
  // Unset (or unreadable): a production build fails closed.
  return nodeEnv !== "production";
}
