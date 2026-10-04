import { afterEach, describe, expect, it, vi } from "vitest";

import {
  BROWSER_SERVICE_NAME,
  cleanAttributes,
  forwardTarget,
  LoadShedder,
  MAX_BODY_BYTES,
  MAX_ITEMS,
  parseOtlpHeaders,
  parseSignal,
  readRelayBody,
  relay,
  RelayError,
  sanitize,
} from "../obs/relay";

const TRACE_ID = "0af7651916cd43dd8448eb211c80319c";
const SPAN_ID = "b7ad6b7169203331";
const API_ENV = { NEXT_PUBLIC_API_URL: "https://api.justabill.io" };
const COLLECTOR = { OTEL_EXPORTER_OTLP_ENDPOINT: "https://collector.example/", OTEL_EXPORTER_OTLP_HEADERS: "Authorization=Bearer%20s3cret" };

function str(key: string, value: string) {
  return { key, value: { stringValue: value } };
}

function span(extra: Record<string, unknown> = {}) {
  return {
    traceId: TRACE_ID,
    spanId: SPAN_ID,
    name: "GET",
    kind: 3,
    startTimeUnixNano: "1727600000000000000",
    endTimeUnixNano: "1727600000100000000",
    attributes: [
      str("url.full", "https://api.justabill.io/api/v1/reps?address=1600+Pennsylvania"),
      str("user_agent.original", "Mozilla/5.0"),
      { key: "http.response.status_code", value: { intValue: 200 } },
      str("enduser.id", "abc"),
    ],
    events: [{ timeUnixNano: "1727600000050000000", name: "fetchStart", attributes: [str("session.id", "x")] }],
    status: { code: 2, message: "api_error 500" },
    ...extra,
  };
}

function traces(spans: unknown[], resource = [str("service.name", "justabill-api")]) {
  return JSON.stringify({ resourceSpans: [{ resource: { attributes: resource }, scopeSpans: [{ scope: { name: "x" }, spans }] }] });
}

function logBody(record: Record<string, unknown>) {
  return JSON.stringify({ resourceLogs: [{ scopeLogs: [{ logRecords: [record] }] }] });
}

function post(body: string, headers: Record<string, string> = {}) {
  return new Request("https://justabill.io/api/otel/v1/traces", {
    method: "POST",
    headers: { "content-type": "application/json", cookie: "session=1", "x-forwarded-for": "203.0.113.9", ...headers },
    body,
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("sanitize", () => {
  it("forces the resource and keeps only allowlisted attributes, without queries", () => {
    const out = sanitize("traces", traces([span()]), {
      ...API_ENV,
      VERCEL_GIT_COMMIT_SHA: "abc123",
      VERCEL_ENV: "production",
    });
    const rs = (out.resourceSpans as { resource: unknown; scopeSpans: { spans: Record<string, unknown>[] }[] }[])[0];
    expect(rs.resource).toEqual({
      attributes: [
        str("service.name", BROWSER_SERVICE_NAME),
        str("service.version", "abc123"),
        str("deployment.environment.name", "production"),
      ],
    });
    const s = rs.scopeSpans[0].spans[0];
    expect(s.attributes).toEqual([
      str("url.full", "https://api.justabill.io/api/v1/reps"),
      { key: "http.response.status_code", value: { intValue: "200" } },
    ]);
    expect(s.events).toEqual([{ timeUnixNano: "1727600000050000000", name: "fetchStart", attributes: [] }]);
    expect(s.status).toEqual({ code: 2, message: "api_error 500" });
    expect(JSON.stringify(out)).not.toMatch(/Mozilla|enduser|session|address|justabill-api/);
  });

  it("drops unknown span fields", () => {
    const out = sanitize("traces", traces([span({ links: [{ traceId: TRACE_ID }], traceState: "a=b" })]), {});
    const s = (out.resourceSpans as { scopeSpans: { spans: Record<string, unknown>[] }[] }[])[0].scopeSpans[0].spans[0];
    expect(s).not.toHaveProperty("links");
    expect(s).not.toHaveProperty("traceState");
  });

  it("keeps an exception log record's allowlisted fields", () => {
    const out = sanitize(
      "logs",
      logBody({
        timeUnixNano: 1727600000000000000,
        severityNumber: 17,
        severityText: "ERROR",
        eventName: "exception",
        body: { stringValue: "boom" },
        traceId: TRACE_ID,
        spanId: SPAN_ID,
        attributes: [str("exception.type", "TypeError"), str("exception.stacktrace", "x".repeat(20_000)), str("user.email", "a@b.c")],
      }),
      {}
    );
    const rl = (out.resourceLogs as { resource: unknown; scopeLogs: { logRecords: Record<string, unknown>[] }[] }[])[0];
    expect(rl.resource).toEqual({ attributes: [str("service.name", BROWSER_SERVICE_NAME)] });
    const r = rl.scopeLogs[0].logRecords[0];
    expect(r).toMatchObject({ severityNumber: 17, severityText: "ERROR", eventName: "exception", body: { stringValue: "boom" } });
    expect(r.traceId).toBe(TRACE_ID);
    const attrs = r.attributes as { key: string; value: { stringValue: string } }[];
    expect(attrs.map((a) => a.key)).toEqual(["exception.type", "exception.stacktrace"]);
    expect(attrs[1].value.stringValue).toHaveLength(8192);
  });

  it.each([
    ["not JSON", "hello"],
    ["the wrong signal's shape", logBody({})],
    ["a bad trace ID", traces([span({ traceId: "nope" })])],
    ["a span name with a path", traces([span({ name: "GET /bills/119-hr-1?x=1" })])],
    ["a non-numeric time", traces([span({ startTimeUnixNano: "soon" })])],
    ["no spans", traces([])],
  ])("rejects %s with a 400", (_what, body) => {
    expect(() => sanitize("traces", body, {})).toThrow(expect.objectContaining({ status: 400 }));
  });

  // An old or forged client can post any URL: the relay reduces it itself (#755).
  it.each([
    [
      "an API URL with a bill ID",
      "https://api.justabill.io/api/v1/bills/hr-119-1/vote",
      "https://api.justabill.io/api/v1/bills/{id}/vote",
    ],
    [
      "an API URL with a member ID",
      "https://api.justabill.io/api/v1/me/compare/A000360",
      "https://api.justabill.io/api/v1/me/compare/{memberID}",
    ],
    [
      "an API URL with a scope key",
      "https://api.justabill.io/api/v1/bills/hr-119-1/aggregates/district-KS-3",
      "https://api.justabill.io/api/v1/bills/{id}/aggregates/{scope_key}",
    ],
    ["an unknown API path", "https://api.justabill.io/api/v2/bills/hr-119-1", "https://api.justabill.io/[unknown]"],
    ["a page URL with a bill ID", "https://justabill.io/bills/hr-119-1?tab=text", "https://justabill.io/bills/[id]"],
    [
      "a page's route pattern",
      "https://justabill.io/share/bill/[bill]/[vote]/[member]",
      "https://justabill.io/share/bill/[bill]/[vote]/[member]",
    ],
    ["an unknown page", "https://justabill.io/[unknown]", "https://justabill.io/[unknown]"],
    ["another origin", "https://www.congress.gov/bill/119th-congress/house-bill/1", "https://www.congress.gov/"],
    ["an API path on another origin", "https://evil.example/api/v1/bills/hr-119-1/vote", "https://evil.example/"],
  ])("reduces %s in url.full to its route pattern", (_what, url, want) => {
    const body = traces([span({ attributes: [str("url.full", url)] })]);
    const out = sanitize("traces", body, API_ENV);
    const s = (out.resourceSpans as { scopeSpans: { spans: Record<string, unknown>[] }[] }[])[0].scopeSpans[0].spans[0];
    expect(s.attributes).toEqual([str("url.full", want)]);
  });

  it.each([
    ["a relative URL", "/api/v1/bills/hr-119-1"],
    ["a data URL", "data:text/plain,hr-119-1"],
  ])("drops %s in url.full", (_what, url) => {
    const body = traces([span({ attributes: [str("url.full", url), str("http.request.method", "GET")] })]);
    const out = sanitize("traces", body, API_ENV);
    const s = (out.resourceSpans as { scopeSpans: { spans: Record<string, unknown>[] }[] }[])[0].scopeSpans[0].spans[0];
    expect(s.attributes).toEqual([str("http.request.method", "GET")]);
  });

  it("reduces a log record's http.route to a page route pattern", () => {
    const route = (value: string) => {
      const out = sanitize("logs", logBody({ attributes: [str("http.route", value)] }), API_ENV);
      const rl = (out.resourceLogs as { scopeLogs: { logRecords: { attributes: unknown[] }[] }[] }[])[0];
      return rl.scopeLogs[0].logRecords[0].attributes;
    };
    expect(route("/bills/[id]")).toEqual([str("http.route", "/bills/[id]")]);
    expect(route("/bills/hr-119-1")).toEqual([str("http.route", "/bills/[id]")]);
    expect(route("/api/v1/me/compare/A000360")).toEqual([str("http.route", "/[unknown]")]);
  });

  it("rejects more than MAX_ITEMS spans with a 413", () => {
    const many = Array.from({ length: MAX_ITEMS + 1 }, () => span());
    expect(() => sanitize("traces", traces(many), {})).toThrow(expect.objectContaining({ status: 413 }));
  });
});

describe("helpers", () => {
  it("parses the signal segment", () => {
    expect(parseSignal("traces")).toBe("traces");
    expect(parseSignal("logs")).toBe("logs");
    expect(parseSignal("metrics")).toBeUndefined();
  });

  it("drops non-scalar and malformed attribute values", () => {
    expect(
      cleanAttributes([
        { key: "server.port", value: { arrayValue: { values: [] } } },
        { key: "server.port", value: { intValue: "12x" } },
        { key: "server.address", value: { boolValue: true } },
        { key: "server.port", value: { doubleValue: 443 } },
        "junk",
      ], { apiOrigin: "https://api.justabill.io" })
    ).toEqual([
      { key: "server.address", value: { boolValue: true } },
      { key: "server.port", value: { doubleValue: 443 } },
    ]);
  });

  it("sheds load past the per-minute limit and recovers the next minute", () => {
    let now = 1_000_000;
    const shed = new LoadShedder(2, () => now);
    expect([shed.allow(), shed.allow(), shed.allow()]).toEqual([true, true, false]);
    now += 60_000;
    expect(shed.allow()).toBe(true);
  });

  it("reads the Collector endpoint and headers from the OTEL_* variables", () => {
    expect(forwardTarget({}, "traces")).toBeUndefined();
    expect(forwardTarget({ ...COLLECTOR, OTEL_SDK_DISABLED: "true" }, "traces")).toBeUndefined();
    expect(forwardTarget({ ...COLLECTOR, OTEL_LOGS_EXPORTER: "none" }, "logs")).toBeUndefined();
    expect(forwardTarget(COLLECTOR, "logs")).toEqual({
      url: "https://collector.example/v1/logs",
      headers: { Authorization: "Bearer s3cret", "Content-Type": "application/json" },
    });
    expect(
      forwardTarget(
        { OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: "https://t.example/in", OTEL_EXPORTER_OTLP_TRACES_HEADERS: "x-k=1" },
        "traces"
      )
    ).toEqual({ url: "https://t.example/in", headers: { "x-k": "1", "Content-Type": "application/json" } });
    expect(parseOtlpHeaders("a=1,bad,=x,b=%E0%A4%A,c = 2")).toEqual({ a: "1", c: "2" });
  });
});

describe("readRelayBody", () => {
  it("refuses a body that isn't JSON", async () => {
    await expect(readRelayBody(post("{}", { "content-type": "application/x-protobuf" }))).rejects.toMatchObject({
      status: 415,
    });
  });

  it("refuses a declared length over the cap", async () => {
    const req = post("{}", { "content-length": String(MAX_BODY_BYTES + 1) });
    await expect(readRelayBody(req)).rejects.toBeInstanceOf(RelayError);
  });

  it("refuses a streamed body over the cap", async () => {
    await expect(readRelayBody(post("x".repeat(MAX_BODY_BYTES + 1)))).rejects.toMatchObject({ status: 413 });
  });

  it("reads a body within the cap", async () => {
    await expect(readRelayBody(post('{"a":1}', { "content-type": "application/json; charset=utf-8" }))).resolves.toBe(
      '{"a":1}'
    );
  });
});

describe("relay", () => {
  it("forwards the sanitized body with only the server's headers", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const res = await relay(post(traces([span()])), "traces", new LoadShedder(), COLLECTOR);
    expect(res.status).toBe(200);
    expect(fetchMock).toHaveBeenCalledOnce();
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("https://collector.example/v1/traces");
    expect(init.headers).toEqual({ Authorization: "Bearer s3cret", "Content-Type": "application/json" });
    expect(init.opentelemetry).toEqual({ ignore: true });
    const sent = JSON.parse(init.body);
    expect(sent.resourceSpans[0].resource.attributes[0]).toEqual(str("service.name", BROWSER_SERVICE_NAME));
    expect(init.body).not.toMatch(/203\.0\.113\.9|session=1|address=/);
  });

  it("answers 200 and drops the body without a Collector", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const res = await relay(post(traces([span()])), "traces", new LoadShedder(), {});
    expect(res.status).toBe(200);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("answers 200 when the Collector fails, so browsers don't retry", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("down")));
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const res = await relay(post(traces([span()])), "traces", new LoadShedder(), COLLECTOR);
    expect(res.status).toBe(200);
    expect(warn).toHaveBeenCalledOnce();
  });

  it("answers 404, 429 and 400 for an unknown signal, a busy instance and a bad body", async () => {
    expect((await relay(post("{}"), "metrics", new LoadShedder(), {})).status).toBe(404);
    const busy = await relay(post(traces([span()])), "traces", new LoadShedder(0), {});
    expect(busy.status).toBe(429);
    expect(busy.headers.get("retry-after")).toBe("60");
    expect((await relay(post("nope"), "traces", new LoadShedder(), {})).status).toBe(400);
  });

  it("is what the route handler serves", async () => {
    const { POST } = await import("../../app/api/otel/v1/[signal]/route");
    const res = await POST(post("{}"), { params: Promise.resolve({ signal: "metrics" }) });
    expect(res.status).toBe(404);
  });
});
