// Which console messages the sign-in test counts as Content Security Policy violations (#751).

/**
 * gapi's telemetry beacon. The loader the Firebase popup needs (allowed in script-src) sends it on a
 * sample of page loads, and connect-src blocks it on purpose: we don't want Google's telemetry, and
 * widening the CSP for it isn't worth it. Any query string; only this exact origin and path.
 */
export const GAPI_BEACON = "https://apis.google.com/js/gen_204";

// Chromium names the blocked URL first: "Connecting to '<url>' violates ..." and "Fetch API cannot
// load <url>. Refused to connect ...". The directive quoted after it lists other URLs.
const BLOCKED_CONNECT = /^(?:Connecting to '([^']+)' violates|Fetch API cannot load (\S+?)\.? Refused to connect)/;

function isGapiBeacon(url: string): boolean {
  try {
    const u = new URL(url);
    return u.origin + u.pathname === GAPI_BEACON;
  } catch {
    return false;
  }
}

/**
 * Whether a console message is a CSP violation the test fails on: every one but a blocked
 * connection to {@link GAPI_BEACON}.
 */
export function isCspViolation(text: string): boolean {
  if (!/Content.Security.Policy/i.test(text)) return false;
  const blocked = BLOCKED_CONNECT.exec(text);
  return !(blocked && isGapiBeacon(blocked[1] ?? blocked[2]));
}
