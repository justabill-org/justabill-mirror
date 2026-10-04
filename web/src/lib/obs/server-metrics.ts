// http.server.request.duration for the Next.js server (docs/design/376-web-request-metric.md). Next
// wraps every request it handles in a SERVER span, "BaseServer.handleRequest", and sets the route
// pattern and status code on it when the handler settles. recordServerRoots makes that span
// recorded for every request, sampled or not, and ServerMetricsSpanProcessor turns each one into a
// histogram point, with the attributes the API's obs.HTTPHandler records. Only sampled spans are
// ever exported, so trace sampling and trace cost don't change.

import { SpanKind, type Attributes, type Context, type Histogram, type Link } from "@opentelemetry/api";
import type { PushMetricExporter, ResourceMetrics } from "@opentelemetry/sdk-metrics";
import {
  SamplingDecision,
  type ReadableSpan,
  type Sampler,
  type SamplingResult,
  type SpanProcessor,
} from "@opentelemetry/sdk-trace-base";
import {
  ATTR_ERROR_TYPE,
  ATTR_HTTP_REQUEST_METHOD,
  ATTR_HTTP_RESPONSE_STATUS_CODE,
  ATTR_HTTP_ROUTE,
  ATTR_URL_SCHEME,
  METRIC_HTTP_SERVER_REQUEST_DURATION,
} from "@opentelemetry/semantic-conventions";

import { DURATION_BUCKETS } from "./http";
import { instrument } from "./index";

/** The `next.span_type` of the SERVER span Next.js starts for each request. */
export const NEXT_SERVER_SPAN = "BaseServer.handleRequest";

// The attributes Next.js 16 sets on that span (next/dist/build/templates/app-page-runtime.js and
// app-route.js). A guard test fails if a Next upgrade renames them.
const NEXT_SPAN_TYPE = "next.span_type";
const NEXT_ROUTE = "next.route";
const NEXT_METHOD = "http.method";
const NEXT_STATUS_CODE = "http.status_code";

// The HTTP semconv's known methods; anything else is recorded as _OTHER, as obs/http.go does.
const KNOWN_METHODS = new Set(["CONNECT", "DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT", "TRACE"]);
const OTHER_METHOD = "_OTHER";

// A metric flush from a request waits this long after the last export, so a busy instance
// exports on its 60 s schedule and a quiet one exports at the end of its next request.
const MIN_FLUSH_GAP_MS = 30_000;

function isNextServerSpan(kind: SpanKind, attrs: Attributes): boolean {
  return kind === SpanKind.SERVER && attrs[NEXT_SPAN_TYPE] === NEXT_SERVER_SPAN;
}

class RecordServerRoots implements Sampler {
  constructor(private readonly inner: Sampler) {}

  shouldSample(
    ctx: Context,
    traceId: string,
    spanName: string,
    spanKind: SpanKind,
    attributes: Attributes,
    links: Link[]
  ): SamplingResult {
    const result = this.inner.shouldSample(ctx, traceId, spanName, spanKind, attributes, links);
    if (result.decision !== SamplingDecision.NOT_RECORD || !isNextServerSpan(spanKind, attributes)) return result;
    return { ...result, decision: SamplingDecision.RECORD };
  }

  toString(): string {
    return `RecordServerRoots{${this.inner.toString()}}`;
  }
}

/**
 * Wraps `inner` so that Next's per-request SERVER span is always recorded: where `inner` says
 * NOT_RECORD for it, the span is recorded but not sampled, so span processors see it and no
 * exporter does. Its children see an unsampled parent, so a parent-based `inner` keeps them
 * non-recording. Every other decision is `inner`'s.
 */
export function recordServerRoots(inner: Sampler): Sampler {
  return new RecordServerRoots(inner);
}

function stringAttr(attrs: Attributes, key: string): string | undefined {
  const value = attrs[key];
  return typeof value === "string" && value !== "" ? value : undefined;
}

function statusCode(attrs: Attributes): number | undefined {
  const value = Number(attrs[NEXT_STATUS_CODE]);
  return Number.isInteger(value) && value >= 100 && value <= 599 ? value : undefined;
}

/**
 * The metric's attributes for one of Next's server spans: method, scheme, route pattern, status
 * code and, on a 5xx, error.type. It's a new allowlisted set, never a copy of the span's, so the
 * raw path, the query and any attribute a later Next.js adds stay out.
 */
export function serverMetricAttributes(span: ReadableSpan, scheme: string): Attributes {
  const method = stringAttr(span.attributes, NEXT_METHOD);
  const attrs: Attributes = {
    [ATTR_HTTP_REQUEST_METHOD]: method && KNOWN_METHODS.has(method) ? method : OTHER_METHOD,
    [ATTR_URL_SCHEME]: scheme,
  };
  const route = stringAttr(span.attributes, NEXT_ROUTE) ?? stringAttr(span.attributes, ATTR_HTTP_ROUTE);
  if (route) attrs[ATTR_HTTP_ROUTE] = route;
  const status = statusCode(span.attributes);
  if (status !== undefined) {
    attrs[ATTR_HTTP_RESPONSE_STATUS_CODE] = status;
    if (status >= 500) attrs[ATTR_ERROR_TYPE] = String(status);
  }
  return attrs;
}

function durationHistogram(): Histogram {
  return instrument(METRIC_HTTP_SERVER_REQUEST_DURATION, (meter) =>
    meter.createHistogram(METRIC_HTTP_SERVER_REQUEST_DURATION, {
      unit: "s",
      description: "Duration of HTTP server requests.",
      advice: { explicitBucketBoundaries: DURATION_BUCKETS },
    })
  );
}

/**
 * Vercel's `waitUntil` for the current request: the platform keeps the function alive until the
 * promise settles. Its request context also takes a function returning one (@vercel/otel passes
 * that), but a promise works with every runtime's signature, so that's what we pass.
 */
export type WaitUntil = (promise: Promise<unknown>) => void;

// The request context Vercel's runtime publishes on globalThis, as @vercel/otel reads it
// (src/vercel-request-context/api.ts) for its own trace flush.
const REQUEST_CONTEXT = Symbol.for("@vercel/request-context");

interface RequestContextReader {
  get(): { waitUntil?: WaitUntil } | undefined;
}

/** The current request's `waitUntil` on Vercel, or undefined anywhere else. */
export function vercelWaitUntil(): WaitUntil | undefined {
  const reader = (globalThis as { [REQUEST_CONTEXT]?: RequestContextReader })[REQUEST_CONTEXT];
  return reader?.get()?.waitUntil;
}

/**
 * A metric exporter that notes when it last exported, so a request can tell whether the instance
 * has been quiet for long enough that it should flush.
 */
export class TimedMetricExporter implements PushMetricExporter {
  private last: number;
  readonly selectAggregationTemporality?: PushMetricExporter["selectAggregationTemporality"];
  readonly selectAggregation?: PushMetricExporter["selectAggregation"];

  constructor(
    private readonly inner: PushMetricExporter,
    private readonly now: () => number = Date.now
  ) {
    this.last = now();
    this.selectAggregationTemporality = inner.selectAggregationTemporality?.bind(inner);
    this.selectAggregation = inner.selectAggregation?.bind(inner);
  }

  /** When the last export started, in milliseconds (the construction time before the first). */
  lastExport(): number {
    return this.last;
  }

  export(metrics: ResourceMetrics, resultCallback: Parameters<PushMetricExporter["export"]>[1]): void {
    this.last = this.now();
    this.inner.export(metrics, resultCallback);
  }

  forceFlush(): Promise<void> {
    return this.inner.forceFlush();
  }

  shutdown(): Promise<void> {
    return this.inner.shutdown();
  }
}

/** How ServerMetricsSpanProcessor records and flushes. */
export interface ServerMetricsOptions {
  /** `url.scheme`: "https" on Vercel, "http" elsewhere. */
  scheme: string;
  /** Exports the metrics now: the metric reader's forceFlush. Without it, nothing is flushed. */
  flush?: () => Promise<void>;
  /** When the metrics were last exported, in milliseconds. */
  lastExport?: () => number;
  /** The current request's waitUntil; defaults to Vercel's. */
  waitUntil?: () => WaitUntil | undefined;
  /** The clock, in milliseconds. */
  now?: () => number;
}

/**
 * Records http.server.request.duration for every Next.js server span that ends, sampled or not
 * (recordServerRoots makes them all recorded). After recording, when the metrics haven't been
 * exported for 30 s, it asks Vercel to keep the function alive for a flush, so a point isn't left
 * in an instance that's frozen before its next periodic export.
 */
export class ServerMetricsSpanProcessor implements SpanProcessor {
  private readonly now: () => number;
  private readonly waitUntil: () => WaitUntil | undefined;
  private lastFlush = Number.NEGATIVE_INFINITY;

  constructor(private readonly opts: ServerMetricsOptions) {
    this.now = opts.now ?? Date.now;
    this.waitUntil = opts.waitUntil ?? vercelWaitUntil;
  }

  onStart(): void {}

  onEnd(span: ReadableSpan): void {
    if (!isNextServerSpan(span.kind, span.attributes)) return;
    const [seconds, nanos] = span.duration;
    durationHistogram().record(seconds + nanos / 1e9, serverMetricAttributes(span, this.opts.scheme));
    this.maybeFlush();
  }

  private maybeFlush(): void {
    const { flush, lastExport } = this.opts;
    if (!flush) return;
    const now = this.now();
    const last = Math.max(lastExport?.() ?? Number.NEGATIVE_INFINITY, this.lastFlush);
    if (now - last <= MIN_FLUSH_GAP_MS) return;
    const waitUntil = this.waitUntil();
    if (!waitUntil) return;
    this.lastFlush = now;
    // Telemetry export failures must never fail the request.
    waitUntil(flush().catch(() => undefined));
  }

  forceFlush(): Promise<void> {
    return Promise.resolve();
  }

  shutdown(): Promise<void> {
    return Promise.resolve();
  }
}
