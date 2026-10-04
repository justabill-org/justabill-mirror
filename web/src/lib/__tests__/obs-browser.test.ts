import fs from "fs";
import path from "path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SeverityNumber } from "@opentelemetry/api-logs";

import {
  BrowserObs,
  browserObsDisabled,
  browserSampleRatio,
  MAX_ERRORS_PER_PAGE,
  reportError,
  traceAction,
  type ObsWindow,
} from "../obs/browser";
import type { BrowserSdk } from "../obs/browser-sdk";
import { setupTestTelemetry } from "../obs/testing";

type Listener = (e: unknown) => void;

/** A window that records listeners and runs idle callbacks when told to. */
function fakeWindow(readyState: DocumentReadyState = "loading", pathname = "/bills/119-hr-1") {
  const listeners = new Map<string, Listener[]>();
  const idle: (() => void)[] = [];
  const win = {
    location: { origin: "https://justabill.io", pathname },
    document: { readyState },
    addEventListener: vi.fn((type: string, fn: Listener) => {
      listeners.set(type, [...(listeners.get(type) ?? []), fn]);
    }),
    requestIdleCallback: vi.fn((fn: () => void) => {
      idle.push(fn);
      return 1;
    }),
  };
  return {
    win: win as unknown as ObsWindow,
    fire: (type: string, e: unknown = {}) => listeners.get(type)?.forEach((fn) => fn(e)),
    runIdle: () => idle.splice(0).forEach((fn) => fn()),
    listeners,
  };
}

function fakeSdk() {
  const sdk = {
    logException: vi.fn(),
    withSpan: vi.fn((_name: string, fn: () => unknown) => Promise.resolve(fn())),
  };
  const setupBrowserSdk = vi.fn(() => sdk as unknown as BrowserSdk);
  return { sdk, setupBrowserSdk, load: vi.fn(() => Promise.resolve({ setupBrowserSdk })) };
}

const ENV = { NEXT_PUBLIC_API_URL: "https://api.justabill.io" };

afterEach(() => {
  vi.restoreAllMocks();
});

describe("browser config", () => {
  it("is disabled only by NEXT_PUBLIC_OTEL_DISABLED=true", () => {
    expect(browserObsDisabled({ NEXT_PUBLIC_OTEL_DISABLED: " TRUE " })).toBe(true);
    expect(browserObsDisabled({ NEXT_PUBLIC_OTEL_DISABLED: "false" })).toBe(false);
    expect(browserObsDisabled({})).toBe(false);
  });

  it("samples 10% in production, all elsewhere, unless the ratio is set", () => {
    expect(browserSampleRatio({ NEXT_PUBLIC_VERCEL_ENV: "production" })).toBe(0.1);
    expect(browserSampleRatio({ NEXT_PUBLIC_VERCEL_ENV: "preview" })).toBe(1);
    expect(browserSampleRatio({ NEXT_PUBLIC_OTEL_TRACES_SAMPLER_ARG: "0.25" })).toBe(0.25);
    expect(browserSampleRatio({ NEXT_PUBLIC_OTEL_TRACES_SAMPLER_ARG: "2", NEXT_PUBLIC_VERCEL_ENV: "production" })).toBe(0.1);
    expect(browserSampleRatio({ NEXT_PUBLIC_OTEL_TRACES_SAMPLER_ARG: "lots" })).toBe(1);
  });

  it("keeps OpenTelemetry out of the modules every page loads", () => {
    // browser.ts is in every page's bundle; only browser-sdk.ts, loaded by import(), may pull in the SDK.
    for (const file of ["browser.ts", "routes.ts", "relay-url.ts", "../../instrumentation-client.ts"]) {
      const src = fs.readFileSync(path.resolve(__dirname, "../obs", file), "utf8");
      expect(src, file).not.toMatch(/from "@opentelemetry|import\s*\(\s*"@opentelemetry/);
      expect(src, file).not.toMatch(/^import (?!type).*"\.\/browser-sdk"/m);
    }
  });
});

describe("BrowserObs", () => {
  it("loads nothing and listens to nothing when disabled", async () => {
    const { win, runIdle } = fakeWindow("complete");
    const { load } = fakeSdk();
    const obs = new BrowserObs(win, { ...ENV, NEXT_PUBLIC_OTEL_DISABLED: "true" }, load);
    obs.start();
    runIdle();
    obs.report(new Error("x"));
    await expect(obs.trace("vote.cast", () => Promise.resolve(7))).resolves.toBe(7);
    expect(win.addEventListener).not.toHaveBeenCalled();
    expect(load).not.toHaveBeenCalled();
  });

  it("loads the SDK only when idle after the load event", async () => {
    const { win, fire, runIdle } = fakeWindow("loading");
    const { load, setupBrowserSdk } = fakeSdk();
    new BrowserObs(win, ENV, load).start();
    expect(load).not.toHaveBeenCalled();
    fire("load");
    expect(load).not.toHaveBeenCalled();
    runIdle();
    await vi.waitFor(() => expect(setupBrowserSdk).toHaveBeenCalled());
    expect(setupBrowserSdk).toHaveBeenCalledWith({
      origin: "https://justabill.io",
      apiOrigin: "https://api.justabill.io",
      sampleRatio: 1,
    });
  });

  it("falls back to a timeout without requestIdleCallback", async () => {
    vi.useFakeTimers();
    try {
      const { win } = fakeWindow("complete");
      (win as { requestIdleCallback?: unknown }).requestIdleCallback = undefined;
      const { load } = fakeSdk();
      new BrowserObs(win, ENV, load).start();
      expect(load).not.toHaveBeenCalled();
      await vi.runAllTimersAsync();
      expect(load).toHaveBeenCalledOnce();
    } finally {
      vi.useRealTimers();
    }
  });

  it("holds errors until the SDK loads, then exports each distinct one once, at most 5", async () => {
    const { win, fire } = fakeWindow("complete");
    const { load, sdk } = fakeSdk();
    const obs = new BrowserObs(win, ENV, load);
    obs.start();
    const boom = new TypeError("boom");
    fire("error", { error: boom, message: "boom", filename: "https://justabill.io/_next/x.js" });
    fire("error", { error: boom });
    fire("unhandledrejection", { reason: new Error("rejected") });
    fire("error", { error: null, message: "Script error.", filename: "" });
    expect(sdk.logException).not.toHaveBeenCalled();

    await obs.loadSdk();
    expect(sdk.logException.mock.calls).toEqual([
      [boom, "/bills/[id]"],
      [expect.objectContaining({ message: "rejected" }), "/bills/[id]"],
    ]);

    for (let i = 0; i < 10; i++) obs.report(new Error(`e${i}`));
    expect(sdk.logException).toHaveBeenCalledTimes(MAX_ERRORS_PER_PAGE);
  });

  it("reports an error event without an error object by its message", async () => {
    const { win, fire } = fakeWindow("complete");
    const { load, sdk } = fakeSdk();
    const obs = new BrowserObs(win, ENV, load);
    obs.start();
    await obs.loadSdk();
    fire("error", { error: undefined, message: "ResizeObserver loop", filename: "https://justabill.io/" });
    expect(sdk.logException).toHaveBeenCalledWith("ResizeObserver loop", "/bills/[id]");
  });

  it("drops held errors and stays quiet when the SDK chunk fails to load", async () => {
    const { win } = fakeWindow("complete");
    const load = vi.fn(() => Promise.reject(new Error("blocked")));
    const obs = new BrowserObs(win, ENV, load);
    obs.report(new Error("before"));
    await obs.loadSdk();
    await obs.loadSdk();
    obs.report(new Error("after"));
    expect(load).toHaveBeenCalledOnce();
    await expect(obs.trace("vote.cast", () => Promise.resolve("ok"))).resolves.toBe("ok");
  });

  it("wraps actions in a span with the route pattern once loaded", async () => {
    const { win } = fakeWindow("complete", "/vote");
    const { load, sdk } = fakeSdk();
    const obs = new BrowserObs(win, ENV, load);
    await expect(obs.trace("vote.cast", () => Promise.resolve(1))).resolves.toBe(1);
    expect(sdk.withSpan).not.toHaveBeenCalled();
    await obs.loadSdk();
    await expect(obs.trace("vote.cast", () => Promise.resolve(2))).resolves.toBe(2);
    expect(sdk.withSpan).toHaveBeenCalledWith("vote.cast", expect.any(Function), { "http.route": "/vote" });
  });
});

describe("module helpers on the server", () => {
  it("traceAction just runs the action", async () => {
    await expect(traceAction("compare.run", () => Promise.resolve("done"))).resolves.toBe("done");
  });

  it("reportError logs to the console and writes an exception record with the route", async () => {
    const tel = setupTestTelemetry();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      const err = new Error("no congresses");
      reportError(err, "/");
      expect(error).toHaveBeenCalledWith(err);
      await vi.waitFor(() => expect(tel.logs()).toHaveLength(1));
      const record = tel.logs()[0];
      expect(record.severityNumber).toBe(SeverityNumber.ERROR);
      expect(record.attributes).toMatchObject({ "exception.message": "no congresses", "http.route": "/" });
    } finally {
      await tel.shutdown();
    }
  });
});
