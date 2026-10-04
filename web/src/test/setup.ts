import { context, metrics, propagation, trace } from "@opentelemetry/api";
import { logs } from "@opentelemetry/api-logs";
import { configure } from "@testing-library/react";
import { afterAll, vi } from "vitest";

// findBy* and waitFor give up after 1s by default, too short on a busy CI runner, where one
// test file can take 40s (a follows.test.tsx waitFor flaked that way on PR #535).
configure({ asyncUtilTimeout: 5_000 });

// vitest.config.ts sets `isolate: false` (#564): test files share a worker, its module cache and,
// for jsdom files, one window. vi.mock doesn't carry over, but modules do, so a page an earlier
// file imported keeps that file's mocked (or real) `../api` and ignores this file's vi.mock. This
// setup file runs before each test file: clearing the module cache gives it fresh imports.
vi.resetModules();

// What a file changed outside its modules would also outlive it, so undo it when the file ends.
afterAll(() => {
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  // The OTel API keeps its providers on globalThis, and registering one fails while another is set.
  trace.disable();
  metrics.disable();
  logs.disable();
  propagation.disable();
  context.disable();
  if (typeof document !== "undefined") resetWindow();
});

/** Puts the shared jsdom window back as a new one starts: storage, cookies, URL and document. */
function resetWindow() {
  localStorage.clear();
  sessionStorage.clear();
  window.history.replaceState(null, "", "/");
  for (const cookie of document.cookie.split(";")) {
    const name = cookie.split("=")[0].trim();
    if (name) document.cookie = `${name}=; Max-Age=0; Path=/; Secure`;
  }
  const html = document.documentElement;
  for (const { name } of [...html.attributes]) html.removeAttribute(name);
  document.head.replaceChildren();
  document.body.replaceChildren();
}
