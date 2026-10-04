import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SpanKind, SpanStatusCode, trace } from "@opentelemetry/api";
import { SeverityNumber } from "@opentelemetry/api-logs";
import { FetchInstrumentation } from "@vercel/otel";

import { logException, withSpan } from "../obs";
import { tracedFetch } from "../obs/http";
import { ATTR_JOB_NAME } from "../obs/names";
import {
  enabledSignals,
  otelConfig,
  propagateContextUrls,
  registerObs,
  scrubAttributes,
  ScrubSpanProcessor,
  SERVICE_NAME,
  traceSampler,
} from "../obs/server";
import { setupTestTelemetry, type TestTelemetry } from "../obs/testing";

const mockFetch = vi.fn();
vi.stubGlobal("fetch", mockFetch);

const { getBill, ApiError } = await import("../api");
const { onRequestError } = await import("../../instrumentation");

const API = "http://localhost:8080";
const DURATION = "http.client.request.duration";

function response(status: number, body: object = {}) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? "OK" : "Error",
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
  } as Response;
}

let tel: TestTelemetry;

beforeEach(() => {
  mockFetch.mockReset();
  tel = setupTestTelemetry();
});

afterEach(async () => {
  await tel.shutdown();
});

describe("withSpan", () => {
  it("records a span with the attributes and returns fn's result", async () => {
    const got = await withSpan("compare.run", () => 42, { [ATTR_JOB_NAME]: "x" });
    expect(got).toBe(42);
    const [span] = tel.spans();
    expect(span.name).toBe("compare.run");
    expect(span.kind).toBe(SpanKind.INTERNAL);
    expect(span.attributes[ATTR_JOB_NAME]).toBe("x");
    expect(span.status.code).toBe(SpanStatusCode.UNSET);
  });

  it("makes the span active inside fn", async () => {
    await withSpan("outer", async () => {
      expect(trace.getActiveSpan()).toBeDefined();
      await withSpan("inner", () => undefined);
    });
    const [inner, outer] = tel.spans();
    expect(inner.parentSpanContext?.spanId).toBe(outer.spanContext().spanId);
  });

  it("marks the span failed and rethrows", async () => {
    await expect(
      withSpan("vote.cast", () => {
        throw new TypeError("boom");
      })
    ).rejects.toThrow("boom");
    const [span] = tel.spans();
    expect(span.status.code).toBe(SpanStatusCode.ERROR);
    expect(span.events.map((e) => e.name)).toContain("exception");
  });
});

describe("tracedFetch", () => {
  it("makes one CLIENT span without the query and sends traceparent", async () => {
    mockFetch.mockResolvedValueOnce(response(200));
    await tracedFetch(`${API}/api/v1/reps?address=1600+Pennsylvania+Ave`, { headers: { "X-Dev-User-Id": "u1" } });

    const [span] = tel.spans();
    expect(span.name).toBe("GET");
    expect(span.kind).toBe(SpanKind.CLIENT);
    expect(span.attributes).toMatchObject({
      "http.request.method": "GET",
      "server.address": "localhost",
      "server.port": 8080,
      "http.response.status_code": 200,
      "url.full": `${API}/api/v1/reps`,
    });
    expect(JSON.stringify(span.attributes)).not.toContain("Pennsylvania");
    expect(span.status.code).toBe(SpanStatusCode.UNSET);

    const init = mockFetch.mock.calls[0][1] as RequestInit & { opentelemetry?: { ignore: boolean } };
    const headers = init.headers as Record<string, string>;
    expect(headers["X-Dev-User-Id"]).toBe("u1");
    expect(headers.traceparent).toContain(span.spanContext().traceId);
    expect(headers.traceparent).toContain(span.spanContext().spanId);
    // @vercel/otel's fetch instrumentation skips it, so there's one span per call.
    expect(init.opentelemetry).toEqual({ ignore: true });
  });

  it("keeps headers passed as a Headers object or a list of pairs", async () => {
    mockFetch.mockResolvedValue(response(200));
    await tracedFetch(`${API}/api/v1/bills`, { headers: new Headers({ "X-Dev-User-Id": "u1" }) });
    await tracedFetch(`${API}/api/v1/bills`, { headers: [["X-Dev-User-Id", "u2"]] });

    const sent = mockFetch.mock.calls.map(([, init]) => (init as RequestInit).headers as Record<string, string>);
    expect(sent.map((h) => h["x-dev-user-id"])).toEqual(["u1", "u2"]);
    expect(sent.every((h) => h.traceparent)).toBe(true);
  });

  it("records http.client.request.duration", async () => {
    mockFetch.mockResolvedValueOnce(response(200));
    await tracedFetch(`${API}/api/v1/bills`, { method: "post" });
    const metric = await tel.metric(DURATION);
    expect(metric?.descriptor.unit).toBe("s");
    const [point] = metric?.dataPoints ?? [];
    expect(point.attributes).toEqual({
      "http.request.method": "POST",
      "server.address": "localhost",
      "server.port": 8080,
      "http.response.status_code": 200,
    });
  });

  it.each([404, 500])("marks a %i response as an api_error", async (status) => {
    mockFetch.mockResolvedValueOnce(response(status));
    const res = await tracedFetch(`${API}/api/v1/bills/nope`);
    expect(res.status).toBe(status);

    const [span] = tel.spans();
    expect(span.status.code).toBe(SpanStatusCode.ERROR);
    expect(span.status.message).toBe(`api_error ${status}`);
    expect(span.attributes["error.type"]).toBe(String(status));
    const [point] = await tel.points(DURATION);
    expect(point.attributes["error.type"]).toBe(String(status));
    expect(point.attributes["http.response.status_code"]).toBe(status);
  });

  it("records a network failure and rethrows it", async () => {
    mockFetch.mockRejectedValueOnce(new TypeError("fetch failed"));
    await expect(tracedFetch(`${API}/health`)).rejects.toThrow("fetch failed");
    const [span] = tel.spans();
    expect(span.status.code).toBe(SpanStatusCode.ERROR);
    expect(span.attributes["error.type"]).toBe("TypeError");
    const [point] = await tel.points(DURATION);
    expect(point.attributes["error.type"]).toBe("TypeError");
  });

  it("uses the scheme's default port", async () => {
    mockFetch.mockResolvedValueOnce(response(200));
    await tracedFetch("https://api.justabill.io/api/v1/bills");
    expect(tel.spans()[0].attributes["server.port"]).toBe(443);
  });

  it("is a child of the active span", async () => {
    mockFetch.mockResolvedValueOnce(response(200));
    await withSpan("render", () => tracedFetch(`${API}/health`));
    const [client, parent] = tel.spans();
    expect(client.parentSpanContext?.spanId).toBe(parent.spanContext().spanId);
  });
});

describe("api.ts", () => {
  it("sends every API call through a client span", async () => {
    mockFetch.mockResolvedValueOnce(response(404, { error: "not found" }));
    await expect(getBill("119-hr-99999")).rejects.toBeInstanceOf(ApiError);
    const [span] = tel.spans();
    expect(span.kind).toBe(SpanKind.CLIENT);
    expect(span.status.code).toBe(SpanStatusCode.ERROR);
    expect(span.attributes["url.full"]).toBe(`${API}/api/v1/bills/119-hr-99999`);
  });
});

describe("exceptions", () => {
  it("logException writes an ERROR exception record with the route", () => {
    logException(new RangeError("bad page"), "/bills/[id]");
    const [record] = tel.logs();
    expect(record.eventName).toBe("exception");
    expect(record.severityNumber).toBe(SeverityNumber.ERROR);
    expect(record.attributes).toMatchObject({
      "exception.type": "RangeError",
      "exception.message": "bad page",
      "http.route": "/bills/[id]",
    });
    expect(record.attributes["exception.stacktrace"]).toContain("RangeError: bad page");
  });

  it("logException carries the active trace", async () => {
    await withSpan("render", () => logException("plain string"));
    const [record] = tel.logs();
    expect(record.attributes["exception.type"]).toBe("string");
    expect(record.spanContext?.traceId).toBe(tel.spans()[0].spanContext().traceId);
  });

  it("onRequestError logs the route pattern, not the path or headers", async () => {
    await onRequestError(
      new Error("render failed"),
      { path: "/bills/119-hr-1?address=secret", method: "GET", headers: { cookie: "dev-user-id=u1" } },
      { routerKind: "App Router", routePath: "/bills/[id]", routeType: "render", revalidateReason: undefined }
    );
    const [record] = tel.logs();
    expect(record.attributes["http.route"]).toBe("/bills/[id]");
    expect(JSON.stringify(record.attributes)).not.toMatch(/secret|119-hr-1|dev-user-id/);
  });
});

describe("server setup", () => {
  const endpoint = { OTEL_EXPORTER_OTLP_ENDPOINT: "https://otel.example" };

  it("is off without an endpoint or when disabled", () => {
    const off = { traces: false, metrics: false, logs: false };
    expect(enabledSignals({})).toEqual(off);
    expect(enabledSignals({ ...endpoint, OTEL_SDK_DISABLED: "true" })).toEqual(off);
    expect(enabledSignals({ ...endpoint, OTEL_SDK_DISABLED: "TRUE " })).toEqual(off);
    expect(otelConfig({})).toBeUndefined();
    expect(otelConfig({ ...endpoint, OTEL_SDK_DISABLED: "true" })).toBeUndefined();
  });

  it("registers nothing when off", async () => {
    await tel.shutdown();
    expect(registerObs({})).toBe(false);
    const span = trace.getTracer("t").startSpan("x");
    expect(span.isRecording()).toBe(false);
    tel = setupTestTelemetry();
  });

  it("turns signals on per endpoint and off per exporter", () => {
    expect(enabledSignals(endpoint)).toEqual({ traces: true, metrics: true, logs: true });
    expect(enabledSignals({ ...endpoint, OTEL_METRICS_EXPORTER: "none" })).toEqual({
      traces: true,
      metrics: false,
      logs: true,
    });
    expect(enabledSignals({ OTEL_EXPORTER_OTLP_LOGS_ENDPOINT: "https://otel.example/v1/logs" })).toEqual({
      traces: false,
      metrics: false,
      logs: true,
    });
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    expect(enabledSignals({ ...endpoint, OTEL_TRACES_EXPORTER: "zipkin" }).traces).toBe(false);
    expect(warn).toHaveBeenCalled();
    warn.mockRestore();
  });

  it("configures exporters only for enabled signals, scrubbing spans first", () => {
    const all = otelConfig(endpoint);
    expect(all?.serviceName).toBe(SERVICE_NAME);
    expect(all?.spanProcessors?.[0]).toBeInstanceOf(ScrubSpanProcessor);
    expect(all?.spanProcessors?.at(-1)).toBe("auto");
    expect(all?.logRecordProcessors).toHaveLength(1);
    expect(all?.metricReaders).toHaveLength(1);

    const logsOnly = otelConfig({ OTEL_EXPORTER_OTLP_LOGS_ENDPOINT: "https://otel.example/v1/logs" });
    expect(logsOnly?.spanProcessors).toEqual([]);
    expect(logsOnly?.metricReaders).toBeUndefined();
    expect(logsOnly?.logRecordProcessors).toHaveLength(1);
  });

  it("samples 10% of new traces in production and all of them elsewhere", () => {
    expect(traceSampler({ VERCEL_ENV: "production" }).toString()).toContain("TraceIdRatioBased{0.1}");
    expect(traceSampler({ VERCEL_ENV: "preview" }).toString()).toContain("TraceIdRatioBased{1}");
    expect(traceSampler({}).toString()).toMatch(/^ParentBased/);
    expect(traceSampler({ OTEL_TRACES_SAMPLER: "always_off" }).toString()).toBe("AlwaysOffSampler");
  });

  it("propagates context to the API origin only", () => {
    const env = { NEXT_PUBLIC_API_URL: "https://api.justabill.io" };
    expect(propagateContextUrls(env)).toEqual(["https://api.justabill.io/"]);

    // The real @vercel/otel matcher, configured as otelConfig configures it.
    const fetchInst = new FetchInstrumentation(otelConfig({ ...endpoint, ...env })?.instrumentationConfig?.fetch);
    const shouldPropagate = (url: string) =>
      (fetchInst as unknown as { shouldPropagate(u: URL): boolean }).shouldPropagate(new URL(url));
    expect(shouldPropagate("https://api.justabill.io/api/v1/bills")).toBe(true);
    expect(shouldPropagate("https://api.congress.gov/v3/bill")).toBe(false);
    expect(shouldPropagate("https://api.justabill.io.evil.example/x")).toBe(false);
    expect(shouldPropagate("http://api.justabill.io/api/v1/bills")).toBe(false);
  });

  it("also propagates context to API_INTERNAL_URL's origin when it's set (#649)", () => {
    const env = { NEXT_PUBLIC_API_URL: "http://localhost:8080", API_INTERNAL_URL: "http://api:8080" };
    expect(propagateContextUrls(env)).toEqual(["http://localhost:8080/", "http://api:8080/"]);
  });

  it("scrubs personal attributes and query strings", () => {
    const attrs = {
      "http.user_agent": "Mozilla",
      "user_agent.original": "Mozilla",
      "http.referer": "https://justabill.io/reps?address=1+Main+St",
      "client.address": "203.0.113.9",
      "url.query": "address=1+Main+St",
      "http.target": "/reps?address=1+Main+St",
      "url.full": "https://api.justabill.io/api/v1/bills?q=tax",
      "http.route": "/reps",
    };
    scrubAttributes(attrs);
    expect(attrs).toEqual({
      "http.target": "/reps",
      "url.full": "https://api.justabill.io/api/v1/bills",
      "http.route": "/reps",
    });
  });
});
