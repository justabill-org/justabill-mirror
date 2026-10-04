// Where the browser reaches the Firebase Auth emulator in the smoke tests (#794), for
// playwright.config.ts. The API and the Node-side helpers use E2E_AUTH_EMULATOR_HOST (in CI,
// auth-emulator:9099, another container). The browser uses the host inlined in the web build
// (NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST), which must be on the pages' own host: with the emulator
// on another site, a popup that opens before the helper iframe asks for apis.google.com/js/api.js
// leaves that request never sent, and the sign-in stalls (about 1 in 60 in CI before #794).

/** The issue that found the cross-site stall, linked from the guard's message. */
export const CROSS_SITE_STALL_ISSUE = "https://github.com/justabill-org/justabill/issues/794";

/** How the run reaches the emulator: the browser's host, and a forwarder when the API's differs. */
export type AuthEmulatorTopology = {
  /** host:port the web build's browser connects to. */
  browserHost: string;
  /** e2e/forward.mjs's arguments: listen on localhost:port, pass on to target (host:port). */
  forward?: { port: string; target: string };
};

/** Parses "host:port" with an explicit port, or throws naming `what`. */
function hostPort(value: string, what: string): URL {
  let url: URL | undefined;
  try {
    url = new URL(`http://${value}`);
  } catch {
    // Reported below.
  }
  if (!url || url.port === "" || url.pathname !== "/" || `${url.hostname}:${url.port}` !== value.toLowerCase()) {
    throw new Error(`${what} (${value}) is not host:port`);
  }
  return url;
}

/**
 * Checks that the browser reaches the emulator on the same host as `webUrl` (stricter than the same
 * site, and all the smoke tests need), and says whether a forwarder must bridge that host to
 * `apiHost`. Throws, so the run stops before any test, when the browser's host is missing, on
 * another host than the pages, or on the pages' host but a different emulator than `apiHost`.
 */
export function authEmulatorTopology(
  apiHost: string,
  browserHost: string | undefined,
  webUrl: string,
): AuthEmulatorTopology {
  const api = hostPort(apiHost, "E2E_AUTH_EMULATOR_HOST");
  if (!browserHost?.trim()) {
    throw new Error(
      "NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST is not set: give the tests the emulator host " +
        "the web build was made with",
    );
  }
  const browser = hostPort(browserHost.trim(), "NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST");
  const pages = new URL(webUrl).hostname;
  if (browser.hostname !== pages) {
    throw new Error(
      `The web build's browser reaches the Auth emulator at ${browser.host}, another site than the pages (${pages}): ` +
        `popup sign-ins stall that way (${CROSS_SITE_STALL_ISSUE}). Build with ` +
        `NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST=${pages}:<port>; the tests forward that port to ` +
        "E2E_AUTH_EMULATOR_HOST.",
    );
  }
  if (api.hostname === pages) {
    if (api.host !== browser.host) {
      throw new Error(
        `E2E_AUTH_EMULATOR_HOST (${api.host}) and the web build's NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST ` +
          `(${browser.host}) are different emulators on ${pages}: the browser's sessions wouldn't reach the API`,
      );
    }
    return { browserHost: browser.host };
  }
  return { browserHost: browser.host, forward: { port: browser.port, target: api.host } };
}
