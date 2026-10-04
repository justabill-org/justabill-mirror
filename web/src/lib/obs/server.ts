// The Next.js server's OpenTelemetry setup (design docs/design/53-observability.md, "The obs
// library → TypeScript"), called once from src/instrumentation.ts. It mirrors the Go obs module:
// configured by the standard OTEL_* variables, and inert without an OTLP endpoint.

import type { Attributes } from "@opentelemetry/api";
import { OTLPLogExporter } from "@opentelemetry/exporter-logs-otlp-proto";
import { OTLPMetricExporter } from "@opentelemetry/exporter-metrics-otlp-proto";
import { BatchLogRecordProcessor, type LogRecordProcessor } from "@opentelemetry/sdk-logs";
import { PeriodicExportingMetricReader, type MetricReader } from "@opentelemetry/sdk-metrics";
import {
  AlwaysOffSampler,
  AlwaysOnSampler,
  ParentBasedSampler,
  TraceIdRatioBasedSampler,
  type ReadableSpan,
  type Sampler,
  type SpanProcessor,
} from "@opentelemetry/sdk-trace-base";
import { registerOTel, type Configuration } from "@vercel/otel";

import { SCOPE } from "./index";
import { recordServerRoots, ServerMetricsSpanProcessor, TimedMetricExporter } from "./server-metrics";

type Env = Record<string, string | undefined>;

/** The service.name of the Next.js server. OTEL_SERVICE_NAME overrides it. */
export const SERVICE_NAME = SCOPE;

// Root sampling ratios, as in obs/resource.go: production keeps 10% of new traces, everything
// else keeps them all.
const PRODUCTION_SAMPLE_RATIO = 0.1;
const DEFAULT_SAMPLE_RATIO = 1;

// Metrics are pushed every 60 s (design 53's default; OTEL_METRIC_EXPORT_INTERVAL overrides it).
// A Vercel instance can be frozen between requests, so ServerMetricsSpanProcessor also flushes at
// the end of a request after a quiet spell (docs/design/376-web-request-metric.md).
const METRIC_EXPORT_INTERVAL_MS = 60_000;

/** Which signals are exported over OTLP. */
export interface Signals {
  traces: boolean;
  metrics: boolean;
  logs: boolean;
}

function signalEnabled(env: Env, signal: "TRACES" | "METRICS" | "LOGS"): boolean {
  const exporter = (env[`OTEL_${signal}_EXPORTER`] ?? "").trim();
  if (exporter === "none") return false;
  if (exporter !== "" && exporter !== "otlp") {
    console.warn(`obs: OTEL_${signal}_EXPORTER=${exporter}: only otlp and none are supported`);
    return false;
  }
  return Boolean(env.OTEL_EXPORTER_OTLP_ENDPOINT || env[`OTEL_EXPORTER_OTLP_${signal}_ENDPOINT`]);
}

/**
 * Reads the standard variables as the Go obs module does: OTEL_SDK_DISABLED=true turns everything
 * off, OTEL_<SIGNAL>_EXPORTER=none turns one signal off, and a signal is on only when it has an
 * endpoint (OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT).
 */
export function enabledSignals(env: Env): Signals {
  if ((env.OTEL_SDK_DISABLED ?? "").trim().toLowerCase() === "true") {
    return { traces: false, metrics: false, logs: false };
  }
  return {
    traces: signalEnabled(env, "TRACES"),
    metrics: signalEnabled(env, "METRICS"),
    logs: signalEnabled(env, "LOGS"),
  };
}

function defaultSampler(env: Env): Sampler {
  const ratio = env.VERCEL_ENV === "production" ? PRODUCTION_SAMPLE_RATIO : DEFAULT_SAMPLE_RATIO;
  return new ParentBasedSampler({ root: new TraceIdRatioBasedSampler(ratio) });
}

// OTEL_TRACES_SAMPLER_ARG for the ratio samplers: a number in [0, 1], else the spec's default, 1.
function samplerRatio(env: Env): number {
  const arg = (env.OTEL_TRACES_SAMPLER_ARG ?? "").trim();
  const ratio = Number(arg);
  if (arg === "" || Number.isNaN(ratio) || ratio < 0 || ratio > 1) return DEFAULT_SAMPLE_RATIO;
  return ratio;
}

/**
 * The trace sampler: parent-based with a trace-ID ratio at the root, 10% in Vercel production and
 * 100% in previews and development. OTEL_TRACES_SAMPLER (with OTEL_TRACES_SAMPLER_ARG) picks one
 * of the standard samplers instead, as the Go SDK reads it for the API and the pipeline; an
 * unknown value warns and keeps the default. It's always a Sampler, so otelConfig can wrap it.
 */
export function traceSampler(env: Env): Sampler {
  const name = (env.OTEL_TRACES_SAMPLER ?? "").trim();
  switch (name) {
    case "":
      return defaultSampler(env);
    case "always_on":
      return new AlwaysOnSampler();
    case "always_off":
      return new AlwaysOffSampler();
    case "traceidratio":
      return new TraceIdRatioBasedSampler(samplerRatio(env));
    case "parentbased_always_on":
      return new ParentBasedSampler({ root: new AlwaysOnSampler() });
    case "parentbased_always_off":
      return new ParentBasedSampler({ root: new AlwaysOffSampler() });
    case "parentbased_traceidratio":
      return new ParentBasedSampler({ root: new TraceIdRatioBasedSampler(samplerRatio(env)) });
    default:
      console.warn(`obs: OTEL_TRACES_SAMPLER=${name}: unsupported, using the default sampler`);
      return defaultSampler(env);
  }
}

/** The metric export interval in milliseconds: OTEL_METRIC_EXPORT_INTERVAL when it's valid, else 60 s. */
export function metricExportInterval(env: Env): number {
  const ms = Number((env.OTEL_METRIC_EXPORT_INTERVAL ?? "").trim());
  return Number.isInteger(ms) && ms > 0 ? ms : METRIC_EXPORT_INTERVAL_MS;
}

/**
 * The URL prefixes that get trace context from @vercel/otel's fetch instrumentation: the API
 * origin only, never Congress.gov or any other host. lib/api.ts's own calls propagate through
 * tracedFetch, so this covers any other fetch to the API.
 */
export function propagateContextUrls(env: Env): string[] {
  const api = env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";
  const urls = [`${new URL(api).origin}/`];
  // The server-side API URL lib/api.ts prefers when it's set (`task up`'s web container, #649).
  if (env.API_INTERNAL_URL) urls.push(`${new URL(env.API_INTERNAL_URL).origin}/`);
  return urls;
}

// Attributes that identify a person or can carry an address in a query string. @vercel/otel sets
// the user agent and referer on every root span, and Next.js puts the raw request target (with its
// query) on its server span.
const DROPPED_ATTRIBUTES = [
  "http.user_agent",
  "user_agent.original",
  "http.referer",
  "http.client_ip",
  "client.address",
  "net.peer.ip",
  "url.query",
];
const QUERY_STRIPPED_ATTRIBUTES = ["http.target", "http.url", "url.full"];

/** Removes personal attributes from `attrs` in place and cuts the query off URL attributes. */
export function scrubAttributes(attrs: Attributes): void {
  for (const key of DROPPED_ATTRIBUTES) delete attrs[key];
  for (const key of QUERY_STRIPPED_ATTRIBUTES) {
    const value = attrs[key];
    if (typeof value === "string" && value.includes("?")) attrs[key] = value.slice(0, value.indexOf("?"));
  }
}

/**
 * A span processor that scrubs every span before the exporting processors after it see it
 * (design: "Privacy by allowlist"; the Collector's redaction is the backstop). It must come first.
 */
export class ScrubSpanProcessor implements SpanProcessor {
  onStart(): void {}

  onEnd(span: ReadableSpan): void {
    scrubAttributes(span.attributes);
  }

  forceFlush(): Promise<void> {
    return Promise.resolve();
  }

  shutdown(): Promise<void> {
    return Promise.resolve();
  }
}

/**
 * The @vercel/otel configuration for `env`, or undefined when no signal is exported. With metrics
 * on, Next's server span is recorded for every request and turned into
 * http.server.request.duration, whether or not traces are exported.
 */
export function otelConfig(env: Env): Configuration | undefined {
  const signals = enabledSignals(env);
  if (!signals.traces && !signals.metrics && !signals.logs) return undefined;

  // The OTLP exporters read their endpoint, headers and timeout from the OTEL_* variables.
  const logRecordProcessors: LogRecordProcessor[] | undefined = signals.logs
    ? [new BatchLogRecordProcessor({ exporter: new OTLPLogExporter() })]
    : undefined;

  let metricReaders: MetricReader[] | undefined;
  const spanProcessors: SpanProcessor[] = signals.traces ? [new ScrubSpanProcessor()] : [];
  let sampler = traceSampler(env);
  if (signals.metrics) {
    const exporter = new TimedMetricExporter(new OTLPMetricExporter());
    const reader = new PeriodicExportingMetricReader({ exporter, exportIntervalMillis: metricExportInterval(env) });
    metricReaders = [reader];
    spanProcessors.push(
      new ServerMetricsSpanProcessor({
        scheme: env.VERCEL ? "https" : "http",
        flush: () => reader.forceFlush(),
        lastExport: () => exporter.lastExport(),
      })
    );
    sampler = recordServerRoots(sampler);
  }

  return {
    serviceName: SERVICE_NAME,
    traceSampler: sampler,
    // "auto" is Vercel's own trace export plus OTLP from the OTEL_* variables. Without a traces
    // endpoint there are none: "auto" would otherwise post to localhost:4318.
    spanProcessors: signals.traces ? [...spanProcessors, "auto"] : spanProcessors,
    instrumentationConfig: { fetch: { propagateContextUrls: propagateContextUrls(env) } },
    logRecordProcessors,
    metricReaders,
  };
}

/**
 * Registers the OpenTelemetry SDK for the Next.js server through @vercel/otel, with OTLP exporters
 * for traces, logs and metrics. Without an endpoint, or with OTEL_SDK_DISABLED=true, it registers
 * nothing, so every obs call stays a no-op. Returns whether it registered.
 */
export function registerObs(env: Env = process.env): boolean {
  const config = otelConfig(env);
  if (!config) return false;
  registerOTel(config);
  return true;
}
