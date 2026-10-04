import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { context, SpanKind, trace, type Attributes, type Tracer } from "@opentelemetry/api";
import { AggregationTemporality, InstrumentType, type ResourceMetrics } from "@opentelemetry/sdk-metrics";
import {
  AlwaysOffSampler,
  BasicTracerProvider,
  InMemorySpanExporter,
  ParentBasedSampler,
  SimpleSpanProcessor,
  TraceIdRatioBasedSampler,
  type IdGenerator,
  type SpanProcessor,
} from "@opentelemetry/sdk-trace-base";

import { metricExportInterval, otelConfig, ScrubSpanProcessor, traceSampler } from "../obs/server";
import {
  NEXT_SERVER_SPAN,
  recordServerRoots,
  ServerMetricsSpanProcessor,
  TimedMetricExporter,
  vercelWaitUntil,
  type WaitUntil,
} from "../obs/server-metrics";
import { setupTestTelemetry, type TestTelemetry } from "../obs/testing";

const DURATION = "http.server.request.duration";

// Trace and span IDs from a fixed seed, so the 10% sampling picks the same traces on every run.
function seededIds(seed = 1): IdGenerator {
  let state = seed;
  const hex = (chars: number) => {
    let out = "";
    while (out.length < chars) {
      state = (state * 1103515245 + 12345) % 2 ** 31;
      out += state.toString(16).padStart(8, "0");
    }
    return out.slice(0, chars);
  };
  return { generateTraceId: () => hex(32), generateSpanId: () => hex(16) };
}

interface Request {
  method?: string;
  target?: string;
  status?: number;
  route?: string;
}

/**
 * Starts and ends a span shaped like Next.js 16's per-request SERVER span: the start attributes
 * the template passes to tracer.trace, then the ones its handler sets when it settles.
 */
function serveRequest(tracer: Tracer, req: Request = {}, child?: (t: Tracer) => void) {
  const { method = "GET", target = "/bills/hr-119-1", status = 200, route = "/bills/[id]" } = req;
  const span = tracer.startSpan(`${method} ${route}`, {
    kind: SpanKind.SERVER,
    attributes: {
      "next.span_category": "nextjs",
      "next.span_type": NEXT_SERVER_SPAN,
      "http.method": method,
      "http.target": target,
    },
  });
  if (child) context.with(trace.setSpan(context.active(), span), () => child(tracer));
  span.setAttributes({ "http.status_code": status, "next.rsc": false, "next.route": route, "http.route": route });
  span.end();
  return span;
}

function histogramCount(points: { value: unknown }[]): number {
  return points.reduce((n, p) => n + (p.value as { count: number }).count, 0);
}

let tel: TestTelemetry;

beforeEach(() => {
  tel = setupTestTelemetry();
});

afterEach(async () => {
  await tel.shutdown();
  vi.unstubAllGlobals();
});

describe("a production-configured web server", () => {
  const env = { VERCEL: "1", VERCEL_ENV: "production", OTEL_EXPORTER_OTLP_ENDPOINT: "https://otel.example" };

  it("counts every request and exports only the sampled traces", async () => {
    const config = otelConfig(env);
    expect(config?.traceSampler?.toString()).toBe(
      "RecordServerRoots{ParentBased{root=TraceIdRatioBased{0.1}, remoteParentSampled=AlwaysOnSampler, " +
        "remoteParentNotSampled=AlwaysOffSampler, localParentSampled=AlwaysOnSampler, localParentNotSampled=AlwaysOffSampler}}"
    );
    // The configured processors, with an in-memory exporter where "auto" (Vercel's and OTLP's
    // exporters, which also drop unsampled spans) would be.
    const exporter = new InMemorySpanExporter();
    const processors = (config?.spanProcessors ?? []).map((p) => (p === "auto" ? new SimpleSpanProcessor(exporter) : p));
    const provider = new BasicTracerProvider({
      sampler: config?.traceSampler as ParentBasedSampler,
      idGenerator: seededIds(),
      spanProcessors: processors as SpanProcessor[],
    });
    const tracer = provider.getTracer("next.js");

    const children: boolean[] = [];
    for (let i = 0; i < 100; i++) {
      serveRequest(tracer, {}, (t) => {
        const span = t.startSpan("render route (app) /bills/[id]");
        const root = trace.getActiveSpan()?.spanContext();
        // A child of an unsampled root stays non-recording, so it costs nothing.
        if (root && root.traceFlags === 0) children.push(span.isRecording());
        span.end();
      });
    }

    expect(histogramCount(await tel.points(DURATION))).toBe(100);

    const exported = exporter.getFinishedSpans();
    const roots = exported.filter((s) => s.kind === SpanKind.SERVER);
    expect(roots.length).toBeGreaterThan(0);
    expect(roots.length).toBeLessThan(30);
    expect(exported.every((s) => (s.spanContext().traceFlags & 1) === 1)).toBe(true);
    expect(children.length).toBe(100 - roots.length);
    expect(children.every((recording) => !recording)).toBe(true);
    await provider.shutdown();
  });
});

describe("recordServerRoots", () => {
  const next = { "next.span_type": NEXT_SERVER_SPAN };

  it("records Next's server span where the inner sampler drops it, without sampling it", () => {
    const sampler = recordServerRoots(new AlwaysOffSampler());
    const got = sampler.shouldSample(context.active(), "a".repeat(32), "GET /", SpanKind.SERVER, next, []);
    expect(got.decision).toBe(1); // SamplingDecision.RECORD
  });

  it("leaves every other decision to the inner sampler", () => {
    const sampler = recordServerRoots(new AlwaysOffSampler());
    const drop = (kind: SpanKind, attrs: Attributes) =>
      sampler.shouldSample(context.active(), "a".repeat(32), "x", kind, attrs, []).decision;
    expect(drop(SpanKind.CLIENT, next)).toBe(0);
    expect(drop(SpanKind.SERVER, { "next.span_type": "AppRender.getBodyResult" })).toBe(0);
    expect(drop(SpanKind.SERVER, {})).toBe(0);

    const sampled = recordServerRoots(new TraceIdRatioBasedSampler(1));
    expect(sampled.shouldSample(context.active(), "a".repeat(32), "x", SpanKind.SERVER, next, []).decision).toBe(2);
  });
});

describe("ServerMetricsSpanProcessor", () => {
  function setup(opts: Partial<ConstructorParameters<typeof ServerMetricsSpanProcessor>[0]> = {}) {
    const processor = new ServerMetricsSpanProcessor({ scheme: "https", waitUntil: () => undefined, ...opts });
    const provider = new BasicTracerProvider({ spanProcessors: [new ScrubSpanProcessor(), processor] });
    return { tracer: provider.getTracer("next.js"), provider };
  }

  it("records a failed render with the route pattern and never the path or query", async () => {
    const { tracer } = setup();
    serveRequest(tracer, { status: 500, target: "/bills/hr-119-1?address=1+Main+St" });

    const points = await tel.points(DURATION);
    expect(points).toHaveLength(1);
    expect(points[0].attributes).toEqual({
      "http.request.method": "GET",
      "url.scheme": "https",
      "http.route": "/bills/[id]",
      "http.response.status_code": 500,
      "error.type": "500",
    });
    expect(JSON.stringify(points[0].attributes)).not.toMatch(/hr-119-1|address|Main/);
    const metric = await tel.metric(DURATION);
    expect(metric?.descriptor.unit).toBe("s");
  });

  it("leaves error.type off below 500", async () => {
    const { tracer } = setup({ scheme: "http" });
    serveRequest(tracer, { status: 404 });
    const [point] = await tel.points(DURATION);
    expect(point.attributes).toEqual({
      "http.request.method": "GET",
      "url.scheme": "http",
      "http.route": "/bills/[id]",
      "http.response.status_code": 404,
    });
  });

  it("records an unknown method as _OTHER and leaves out a missing route or status", async () => {
    const { tracer } = setup();
    const span = tracer.startSpan("PROPFIND", {
      kind: SpanKind.SERVER,
      attributes: { "next.span_type": NEXT_SERVER_SPAN, "http.method": "PROPFIND", "http.target": "/wp-admin/x" },
    });
    span.setAttribute("http.status_code", -1);
    span.end();

    const [point] = await tel.points(DURATION);
    expect(point.attributes).toEqual({ "http.request.method": "_OTHER", "url.scheme": "https" });
  });

  it("falls back to http.route when next.route is missing", async () => {
    const { tracer } = setup();
    const span = tracer.startSpan("GET", {
      kind: SpanKind.SERVER,
      attributes: { "next.span_type": NEXT_SERVER_SPAN, "http.method": "get" },
    });
    span.setAttributes({ "http.route": "/api/og", "http.status_code": 200 });
    span.end();

    const [point] = await tel.points(DURATION);
    // Methods are case-sensitive in the HTTP semconv, as in obs/http.go.
    expect(point.attributes["http.request.method"]).toBe("_OTHER");
    expect(point.attributes["http.route"]).toBe("/api/og");
  });

  it("ignores spans that aren't Next's server span", async () => {
    const { tracer } = setup();
    tracer.startSpan("GET", { kind: SpanKind.CLIENT, attributes: { "next.span_type": NEXT_SERVER_SPAN } }).end();
    tracer.startSpan("render", { kind: SpanKind.SERVER, attributes: { "next.span_type": "AppRender.getBodyResult" } }).end();
    expect(await tel.points(DURATION)).toEqual([]);
  });

  it("records the span's duration in seconds", async () => {
    const { tracer } = setup();
    const span = tracer.startSpan("GET /", {
      kind: SpanKind.SERVER,
      startTime: [1000, 0],
      attributes: { "next.span_type": NEXT_SERVER_SPAN, "http.method": "GET" },
    });
    span.end([1001, 500_000_000]);
    const [point] = await tel.points(DURATION);
    expect((point.value as { sum: number }).sum).toBeCloseTo(1.5);
  });
});

describe("the metric flush on Vercel", () => {
  function setup(sinceExport: number) {
    let now = 100_000;
    const flush = vi.fn(() => Promise.resolve());
    const tasks: Promise<unknown>[] = [];
    const waitUntil: WaitUntil = (promise) => tasks.push(promise);
    const processor = new ServerMetricsSpanProcessor({
      scheme: "https",
      flush,
      lastExport: () => 100_000 - sinceExport,
      waitUntil: () => waitUntil,
      now: () => now,
    });
    const provider = new BasicTracerProvider({ spanProcessors: [processor] });
    return {
      tracer: provider.getTracer("next.js"),
      flush,
      tasks,
      advance: (ms: number) => (now += ms),
    };
  }

  it("registers a flush after more than 30 s without an export", async () => {
    const { tracer, flush, tasks } = setup(30_001);
    serveRequest(tracer);
    expect(tasks).toHaveLength(1);
    // The flush has already started: waitUntil gets its promise, not a callback to run later.
    expect(flush).toHaveBeenCalledOnce();
    await tasks[0];
  });

  it("doesn't flush within 30 s of an export", () => {
    const { tracer, flush, tasks } = setup(30_000);
    serveRequest(tracer);
    expect(tasks).toHaveLength(0);
    expect(flush).not.toHaveBeenCalled();
  });

  it("registers one flush for a burst of requests, and another after 30 s", () => {
    const { tracer, tasks, advance } = setup(60_000);
    serveRequest(tracer);
    serveRequest(tracer);
    advance(10_000);
    serveRequest(tracer);
    expect(tasks).toHaveLength(1);
    advance(20_001);
    serveRequest(tracer);
    expect(tasks).toHaveLength(2);
  });

  it("swallows a failed flush", async () => {
    const { tracer, flush, tasks } = setup(60_000);
    flush.mockRejectedValueOnce(new Error("collector down"));
    serveRequest(tracer);
    await expect(tasks[0]).resolves.toBeUndefined();
  });

  it("reads waitUntil from Vercel's request context, and nothing elsewhere", () => {
    expect(vercelWaitUntil()).toBeUndefined();
    const waitUntil = vi.fn();
    vi.stubGlobal(Symbol.for("@vercel/request-context") as unknown as string, { get: () => ({ waitUntil }) });
    expect(vercelWaitUntil()).toBe(waitUntil);
  });
});

describe("TimedMetricExporter", () => {
  it("notes each export and delegates to the real exporter", async () => {
    let now = 5;
    const inner = {
      export: vi.fn((_m: ResourceMetrics, cb: (r: { code: number }) => void) => cb({ code: 0 })),
      forceFlush: vi.fn(() => Promise.resolve()),
      shutdown: vi.fn(() => Promise.resolve()),
      selectAggregationTemporality: () => AggregationTemporality.DELTA,
    };
    const exporter = new TimedMetricExporter(inner, () => now);
    expect(exporter.lastExport()).toBe(5);

    now = 42;
    const cb = vi.fn();
    exporter.export({} as ResourceMetrics, cb);
    expect(exporter.lastExport()).toBe(42);
    expect(cb).toHaveBeenCalledWith({ code: 0 });
    expect(exporter.selectAggregationTemporality?.(InstrumentType.HISTOGRAM)).toBe(AggregationTemporality.DELTA);
    expect(exporter.selectAggregation).toBeUndefined();
    await exporter.forceFlush();
    await exporter.shutdown();
    expect(inner.forceFlush).toHaveBeenCalledOnce();
    expect(inner.shutdown).toHaveBeenCalledOnce();
  });
});

describe("server setup for the metric", () => {
  const endpoint = { OTEL_EXPORTER_OTLP_ENDPOINT: "https://otel.example" };

  it("records the metric when metrics are exported and traces aren't", async () => {
    const config = otelConfig({ ...endpoint, OTEL_TRACES_EXPORTER: "none" });
    expect(config?.spanProcessors).toHaveLength(1);
    expect(config?.spanProcessors?.[0]).toBeInstanceOf(ServerMetricsSpanProcessor);
    expect(config?.traceSampler?.toString()).toMatch(/^RecordServerRoots\{ParentBased/);

    const provider = new BasicTracerProvider({
      sampler: config?.traceSampler as ParentBasedSampler,
      spanProcessors: config?.spanProcessors as SpanProcessor[],
    });
    serveRequest(provider.getTracer("next.js"));
    expect(histogramCount(await tel.points(DURATION))).toBe(1);
  });

  it("keeps the scrubber first and the exporters last when every signal is on", () => {
    const processors = otelConfig(endpoint)?.spanProcessors ?? [];
    expect(processors[0]).toBeInstanceOf(ScrubSpanProcessor);
    expect(processors[1]).toBeInstanceOf(ServerMetricsSpanProcessor);
    expect(processors[2]).toBe("auto");
  });

  it("leaves the sampler alone when metrics are off", () => {
    const config = otelConfig({ ...endpoint, OTEL_METRICS_EXPORTER: "none" });
    expect(config?.traceSampler?.toString()).toMatch(/^ParentBased/);
    expect(config?.spanProcessors).toHaveLength(2);
    expect(config?.metricReaders).toBeUndefined();
  });

  it("builds the standard samplers from OTEL_TRACES_SAMPLER", () => {
    const sampler = (name: string, arg?: string) =>
      traceSampler({ OTEL_TRACES_SAMPLER: name, OTEL_TRACES_SAMPLER_ARG: arg }).toString();
    expect(sampler("always_on")).toBe("AlwaysOnSampler");
    expect(sampler("always_off")).toBe("AlwaysOffSampler");
    expect(sampler("traceidratio", "0.25")).toBe("TraceIdRatioBased{0.25}");
    expect(sampler("traceidratio", "2")).toBe("TraceIdRatioBased{1}");
    expect(sampler("traceidratio")).toBe("TraceIdRatioBased{1}");
    expect(sampler("parentbased_always_on")).toMatch(/^ParentBased\{root=AlwaysOnSampler,/);
    expect(sampler("parentbased_always_off")).toMatch(/^ParentBased\{root=AlwaysOffSampler,/);
    expect(sampler("parentbased_traceidratio", "0.5")).toMatch(/^ParentBased\{root=TraceIdRatioBased\{0.5\},/);

    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    expect(traceSampler({ OTEL_TRACES_SAMPLER: "jaeger_remote", VERCEL_ENV: "production" }).toString()).toMatch(
      /^ParentBased\{root=TraceIdRatioBased\{0.1\},/
    );
    expect(warn).toHaveBeenCalled();
    warn.mockRestore();
  });

  it("exports metrics every 60 s unless OTEL_METRIC_EXPORT_INTERVAL says otherwise", () => {
    expect(metricExportInterval({})).toBe(60_000);
    expect(metricExportInterval({ OTEL_METRIC_EXPORT_INTERVAL: "1000" })).toBe(1000);
    expect(metricExportInterval({ OTEL_METRIC_EXPORT_INTERVAL: "soon" })).toBe(60_000);
    expect(metricExportInterval({ OTEL_METRIC_EXPORT_INTERVAL: "-5" })).toBe(60_000);
  });
});

// The processor reads attributes Next.js sets inside its route templates. If an upgrade renames
// them, the metric would silently lose its route or status (or stop), so this fails first.
describe("Next.js still sets the server span attributes the metric reads", () => {
  const require = createRequire(import.meta.url);
  const templates = path.join(path.dirname(require.resolve("next/package.json")), "dist/build/templates");

  it.each(["app-page-runtime.js", "app-route.js"])("%s", (file) => {
    const source = readFileSync(path.join(templates, file), "utf8");
    expect(source).toContain("BaseServerSpan.handleRequest");
    expect(source).toContain("kind: _tracer.SpanKind.SERVER");
    expect(source).toContain("'http.method': method");
    expect(source).toContain("'http.status_code'");
    expect(source).toContain("'next.route': route");
    expect(source).toContain("rootSpanAttributes.get('next.span_type')");
  });

  it("names the span type as the sampler and processor expect", () => {
    const constants = readFileSync(
      path.join(path.dirname(require.resolve("next/package.json")), "dist/server/lib/trace/constants.js"),
      "utf8"
    );
    expect(constants).toContain('BaseServerSpan["handleRequest"] = "BaseServer.handleRequest"');
    // Only allowlisted span types reach OpenTelemetry at all.
    const allowlist = /NextVanillaSpanAllowlist = new Set\(\[([^\]]*)\]/.exec(constants)?.[1] ?? "";
    expect(allowlist).toContain('"BaseServer.handleRequest"');
    const tracer = readFileSync(
      path.join(path.dirname(require.resolve("next/package.json")), "dist/server/lib/trace/tracer.js"),
      "utf8"
    );
    // The span type is a start attribute, so the sampler sees it.
    expect(tracer).toContain("'next.span_type': type");
  });
});
