import { describe, expect, it } from "vitest";
import { isCspViolation } from "../../../e2e/csp";

// The sign-in e2e test's CSP filter (#751, #792): it may drop gapi's gen_204 beacon and nothing else.

const DIRECTIVE = `"connect-src 'self' http://localhost:18080 http://auth-emulator:9099"`;

describe("isCspViolation", () => {
  it.each([
    // The two messages Chromium logged for the beacon on PR #749 (run 37101142979).
    `Connecting to 'https://apis.google.com/js/gen_204?c=50%3A1' violates the following Content Security Policy directive: ${DIRECTIVE}. The action has been blocked.`,
    "Fetch API cannot load https://apis.google.com/js/gen_204?c=50%3A1. Refused to connect because it violates the document's Content Security Policy.",
    // No query string.
    `Connecting to 'https://apis.google.com/js/gen_204' violates the following Content Security Policy directive: ${DIRECTIVE}. The action has been blocked.`,
  ])("ignores the gen_204 beacon: %s", (text) => {
    expect(isCspViolation(text)).toBe(false);
  });

  it.each([
    // Another connection, to Google or not.
    `Connecting to 'https://apis.google.com/js/api.js' violates the following Content Security Policy directive: ${DIRECTIVE}. The action has been blocked.`,
    "Fetch API cannot load https://evil.example/collect. Refused to connect because it violates the document's Content Security Policy.",
    // The same path on another origin, a longer path, and the beacon only in a query string.
    `Connecting to 'http://apis.google.com/js/gen_204' violates the following Content Security Policy directive: ${DIRECTIVE}. The action has been blocked.`,
    `Connecting to 'https://apis.google.com/js/gen_2045' violates the following Content Security Policy directive: ${DIRECTIVE}. The action has been blocked.`,
    `Connecting to 'https://evil.example/?u=https://apis.google.com/js/gen_204' violates the following Content Security Policy directive: ${DIRECTIVE}. The action has been blocked.`,
    // The beacon's URL, but a script load rather than a connection.
    `Loading the script 'https://apis.google.com/js/gen_204' violates the following Content Security Policy directive: "script-src 'self'". The action has been blocked.`,
    // A violation that names no blocked URL.
    `Executing inline script violates the following Content Security Policy directive 'script-src 'self''. The action has been blocked.`,
  ])("reports any other violation: %s", (text) => {
    expect(isCspViolation(text)).toBe(true);
  });

  it("ignores messages that aren't CSP violations", () => {
    expect(isCspViolation("Failed to load resource: the server responded with a status of 404 (Not Found)")).toBe(false);
    expect(isCspViolation("GET https://apis.google.com/js/gen_204 net::ERR_FAILED")).toBe(false);
  });
});
