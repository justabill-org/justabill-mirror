// Browser telemetry (design docs/design/53-observability.md, Decision 5): page loads, API fetches,
// votes and comparisons as traces, and errors as `exception` log records, all sent to our own
// relay (/api/otel/v1/*). This module is in every page's bundle, so it imports no OpenTelemetry
// code: startBrowserObs loads the SDK (browser-sdk.ts) with a dynamic import once the page has
// loaded, and not at all when NEXT_PUBLIC_OTEL_DISABLED=true. Nothing here identifies a visitor:
// no IDs, cookies or storage, route patterns instead of URLs, and no vote values.

import type { BrowserSdk, BrowserSdkConfig } from "./browser-sdk";
import { routePattern } from "./routes";

/** The span around casting a vote, from the click until it's saved. */
export const SPAN_VOTE_CAST = "vote.cast";
/** The span around removing a vote, from the click until it's gone. */
export const SPAN_VOTE_REMOVE = "vote.remove";
/** The span around comparing the visitor's votes with their representatives'. */
export const SPAN_COMPARE_RUN = "compare.run";

/** The most errors one page load exports; repeats of the same error count once. */
export const MAX_ERRORS_PER_PAGE = 5;

const PRODUCTION_SAMPLE_RATIO = 0.1;

type Env = Record<string, string | undefined>;

/** The public build-time variables browser telemetry reads (inlined by `next build`). */
export function browserEnv(): Env {
  return {
    NEXT_PUBLIC_OTEL_DISABLED: process.env.NEXT_PUBLIC_OTEL_DISABLED,
    NEXT_PUBLIC_OTEL_TRACES_SAMPLER_ARG: process.env.NEXT_PUBLIC_OTEL_TRACES_SAMPLER_ARG,
    NEXT_PUBLIC_VERCEL_ENV: process.env.NEXT_PUBLIC_VERCEL_ENV,
    NEXT_PUBLIC_API_URL: process.env.NEXT_PUBLIC_API_URL,
  };
}

/** Whether browser telemetry is off: NEXT_PUBLIC_OTEL_DISABLED=true. */
export function browserObsDisabled(env: Env): boolean {
  return (env.NEXT_PUBLIC_OTEL_DISABLED ?? "").trim().toLowerCase() === "true";
}

/**
 * The share of new browser traces kept: NEXT_PUBLIC_OTEL_TRACES_SAMPLER_ARG when it's a number
 * from 0 to 1, else 10% in Vercel production and all of them elsewhere, like the server.
 */
export function browserSampleRatio(env: Env): number {
  const arg = env.NEXT_PUBLIC_OTEL_TRACES_SAMPLER_ARG?.trim();
  const ratio = arg ? Number(arg) : NaN;
  if (Number.isFinite(ratio) && ratio >= 0 && ratio <= 1) return ratio;
  return env.NEXT_PUBLIC_VERCEL_ENV === "production" ? PRODUCTION_SAMPLE_RATIO : 1;
}

function errorKey(err: unknown): string {
  if (err instanceof Error) return `${err.name}: ${err.message}`;
  return String(err);
}

/** The page's window, as far as BrowserObs uses it (a narrow type, so tests can fake it). */
export type ObsWindow = Pick<Window, "addEventListener" | "requestIdleCallback"> & {
  location: Pick<Location, "origin" | "pathname">;
  document: Pick<Document, "readyState">;
};

type SdkModule = { setupBrowserSdk(config: BrowserSdkConfig): BrowserSdk };

/**
 * One page's browser telemetry. It exports at most {@link MAX_ERRORS_PER_PAGE} distinct errors,
 * holding the ones reported before the SDK has loaded, and runs actions inside spans once it has.
 */
export class BrowserObs {
  private sdk: BrowserSdk | undefined;
  private failed = false;
  private readonly pending: { err: unknown; route: string }[] = [];
  private readonly seen = new Set<string>();

  constructor(
    private readonly win: ObsWindow,
    private readonly env: Env,
    private readonly load: () => Promise<SdkModule> = () => import("./browser-sdk")
  ) {}

  /**
   * Listens for uncaught errors and rejections, and loads the SDK when the browser is idle after
   * the page's load event, i.e. after hydration. Does nothing when telemetry is disabled.
   */
  start(): void {
    if (browserObsDisabled(this.env)) return;
    this.win.addEventListener("error", (e: ErrorEvent) => {
      // "Script error." with no error is a cross-origin script (an extension, say): no details.
      if (e.error === undefined || e.error === null) {
        if (!e.filename) return;
        this.report(e.message);
        return;
      }
      this.report(e.error);
    });
    this.win.addEventListener("unhandledrejection", (e: PromiseRejectionEvent) => this.report(e.reason));

    const idle = () => {
      if (typeof this.win.requestIdleCallback === "function") this.win.requestIdleCallback(() => void this.loadSdk());
      else setTimeout(() => void this.loadSdk(), 0);
    };
    if (this.win.document.readyState === "complete") idle();
    else this.win.addEventListener("load", idle, { once: true });
  }

  /** Loads and sets up the SDK, then exports the errors reported meanwhile. */
  async loadSdk(): Promise<void> {
    if (this.sdk || this.failed) return;
    try {
      const mod = await this.load();
      this.sdk = mod.setupBrowserSdk({
        origin: this.win.location.origin,
        apiOrigin: new URL(this.env.NEXT_PUBLIC_API_URL || "http://localhost:8080").origin,
        sampleRatio: browserSampleRatio(this.env),
      });
    } catch {
      // A blocked or failed chunk: the page works without telemetry.
      this.failed = true;
      this.pending.length = 0;
      return;
    }
    for (const { err, route } of this.pending.splice(0)) this.sdk.logException(err, route);
  }

  /** Exports `err` as an `exception` log record, unless it's a repeat or the page's quota is used. */
  report(err: unknown): void {
    if (browserObsDisabled(this.env) || this.failed) return;
    const key = errorKey(err);
    if (this.seen.has(key) || this.seen.size >= MAX_ERRORS_PER_PAGE) return;
    this.seen.add(key);
    const route = routePattern(this.win.location.pathname);
    if (this.sdk) this.sdk.logException(err, route);
    else this.pending.push({ err, route });
  }

  /** Runs `fn` inside a span called `name` with the page's route pattern, once the SDK is loaded. */
  trace<T>(name: string, fn: () => Promise<T>): Promise<T> {
    if (!this.sdk) return fn();
    return this.sdk.withSpan(name, () => fn(), { "http.route": routePattern(this.win.location.pathname) });
  }
}

let current: BrowserObs | undefined;

/** Starts the page's browser telemetry; called once from src/instrumentation-client.ts. */
export function startBrowserObs(): void {
  if (current || typeof window === "undefined") return;
  current = new BrowserObs(window, browserEnv());
  current.start();
}

/**
 * Logs a caught error to the console and exports it as an `exception` log record, for error
 * boundaries and the catch blocks that handle an error rather than rethrow it. In the browser it
 * goes through the relay (with the page's route pattern); on the server, through lib/obs's
 * logException with `route`. Never pass user data in `err`'s message.
 */
export function reportError(err: unknown, route?: string): void {
  console.error(err);
  if (typeof window === "undefined") {
    void import("./index").then(({ logException }) => logException(err, route));
    return;
  }
  current?.report(err);
}

/**
 * Runs a user action (a vote, a comparison) inside a span called `name`, so its API fetches share
 * one trace. Before the SDK loads, or when telemetry is off, it just runs `fn`.
 */
export function traceAction<T>(name: string, fn: () => Promise<T>): Promise<T> {
  return current ? current.trace(name, fn) : fn();
}
