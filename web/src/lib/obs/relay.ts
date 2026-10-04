// The browser telemetry relay behind POST /api/otel/v1/{traces,logs} (design
// docs/design/53-observability.md, Decision 5 and "Security and privacy"). The endpoint is public,
// so nothing the browser sends is trusted: the body is rebuilt from an allowlist of OTLP fields and
// attributes, the resource is replaced with ours, and only then is it forwarded to the Collector
// with the server's own OTLP headers (the bearer token). The visitor's cookies, headers and IP are
// never forwarded.

import { apiOriginOf, reduceUrl } from "./relay-url";
import { routePattern } from "./routes";
import { enabledSignals } from "./server";

type Env = Record<string, string | undefined>;

/** The two OTLP signals the browser sends. */
export type RelaySignal = "traces" | "logs";

/** The service.name every relayed record gets, whatever the browser claimed. */
export const BROWSER_SERVICE_NAME = "justabill-browser";

/** The largest body the relay reads. Browsers cap keepalive requests near this size anyway. */
export const MAX_BODY_BYTES = 64 * 1024;

/** The most spans or log records one request may carry. */
export const MAX_ITEMS = 256;

/** Requests one server instance relays per minute before it answers 429 (until #51's WAF rule). */
export const MAX_REQUESTS_PER_MINUTE = 100;

const MAX_EVENTS_PER_SPAN = 64;
const MAX_STRING = 1024;
const MAX_STACKTRACE = 8192;
const FORWARD_TIMEOUT_MS = 10_000;

// The attributes a browser span, span event or log record may keep. Everything else is dropped,
// user agents and anything a future instrumentation might add included.
const ALLOWED_ATTRIBUTES = new Set([
  "http.request.method",
  "http.response.status_code",
  "http.route",
  "url.full",
  "server.address",
  "server.port",
  "error.type",
  "http.request.body.size",
  "http.response_content_length",
  "http.response_content_length_uncompressed",
  "exception.type",
  "exception.message",
  "exception.stacktrace",
]);

// Span and event names are fixed strings from our code or the instrumentations ("GET",
// "documentLoad", "vote.cast"); a name that could carry a path, query or free text is rejected.
const NAME_PATTERN = /^[A-Za-z0-9 ._:-]{1,64}$/;
const TRACE_ID = /^[0-9a-f]{32}$/;
const SPAN_ID = /^[0-9a-f]{16}$/;
const NANOS = /^\d{1,20}$/;

/** A request the relay turned away, with the status to answer. */
export class RelayError extends Error {
  constructor(
    public status: number,
    message: string
  ) {
    super(message);
    this.name = "RelayError";
  }
}

/** Parses the `[signal]` route segment. */
export function parseSignal(segment: string): RelaySignal | undefined {
  return segment === "traces" || segment === "logs" ? segment : undefined;
}

/**
 * Counts requests in fixed one-minute windows and says whether one more fits. One per server
 * instance: it sheds load, it isn't a fair per-client limit (that's the WAF's job).
 */
export class LoadShedder {
  private windowStart = 0;
  private count = 0;

  constructor(
    private readonly limit = MAX_REQUESTS_PER_MINUTE,
    private readonly now: () => number = Date.now
  ) {}

  /** Records a request and returns false when the current minute is already full. */
  allow(): boolean {
    const t = this.now();
    if (t - this.windowStart >= 60_000) {
      this.windowStart = t;
      this.count = 0;
    }
    if (this.count >= this.limit) return false;
    this.count++;
    return true;
  }
}

/**
 * Reads the request body as text, refusing (413) one over MAX_BODY_BYTES without reading the rest
 * and (415) one that isn't OTLP/HTTP JSON.
 */
export async function readRelayBody(request: Request): Promise<string> {
  const type = (request.headers.get("content-type") ?? "").split(";")[0].trim().toLowerCase();
  if (type !== "application/json") throw new RelayError(415, "only OTLP/HTTP JSON is accepted");
  const declared = Number(request.headers.get("content-length") ?? "0");
  if (declared > MAX_BODY_BYTES) throw new RelayError(413, `body over ${MAX_BODY_BYTES} bytes`);
  if (!request.body) return "";

  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > MAX_BODY_BYTES) {
      await reader.cancel();
      throw new RelayError(413, `body over ${MAX_BODY_BYTES} bytes`);
    }
    chunks.push(value);
  }
  return new TextDecoder().decode(Buffer.concat(chunks));
}

type Json = Record<string, unknown>;
type AnyValue = { stringValue: string } | { boolValue: boolean } | { intValue: string } | { doubleValue: number };
type KeyValue = { key: string; value: AnyValue };

function invalid(what: string): never {
  throw new RelayError(400, `not an OTLP body: ${what}`);
}

function isObject(v: unknown): v is Json {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function array(v: unknown, what: string): unknown[] {
  if (v === undefined) return [];
  if (!Array.isArray(v)) invalid(what);
  return v;
}

function nanos(v: unknown, what: string): string {
  // The SDKs send strings; a number (no BigInt) has already lost precision past 2^53, which is fine.
  const s = typeof v === "number" && Number.isInteger(v) && v >= 0 ? BigInt(v).toString() : v;
  if (typeof s !== "string" || !NANOS.test(s)) invalid(what);
  return s;
}

function optionalId(v: unknown, pattern: RegExp, what: string): string | undefined {
  if (v === undefined || v === "") return undefined;
  if (typeof v !== "string" || !pattern.test(v)) invalid(what);
  return v;
}

function name(v: unknown, what: string): string {
  if (typeof v !== "string" || !NAME_PATTERN.test(v)) invalid(what);
  return v;
}

/** What the relay needs to know to scrub attributes. */
export interface ScrubContext {
  /** The API's origin, whose URLs keep their API route pattern. */
  apiOrigin: string;
}

// Reduces the attributes that hold a URL or route to a route pattern, whatever the browser sent,
// so an old or forged client can't store a bill or member ID either. Undefined drops it.
function cleanString(key: string, value: string, ctx: ScrubContext): string | undefined {
  if (key === "url.full") return reduceUrl(value, undefined, ctx.apiOrigin)?.slice(0, MAX_STRING);
  if (key === "http.route") return routePattern(value);
  return value.slice(0, key === "exception.stacktrace" ? MAX_STACKTRACE : MAX_STRING);
}

function cleanValue(key: string, value: unknown, ctx: ScrubContext): AnyValue | undefined {
  if (!isObject(value)) return undefined;
  if (typeof value.stringValue === "string") {
    const s = cleanString(key, value.stringValue, ctx);
    return s === undefined ? undefined : { stringValue: s };
  }
  if (typeof value.boolValue === "boolean") return { boolValue: value.boolValue };
  if (typeof value.intValue === "number" && Number.isSafeInteger(value.intValue)) {
    return { intValue: String(value.intValue) };
  }
  if (typeof value.intValue === "string" && /^-?\d{1,19}$/.test(value.intValue)) return { intValue: value.intValue };
  if (typeof value.doubleValue === "number" && Number.isFinite(value.doubleValue)) {
    return { doubleValue: value.doubleValue };
  }
  return undefined;
}

/**
 * Keeps the allowlisted attributes with scalar values, strings cut to size, and URLs and routes
 * reduced to route patterns.
 */
export function cleanAttributes(attrs: unknown, ctx: ScrubContext): KeyValue[] {
  const out: KeyValue[] = [];
  for (const kv of array(attrs, "attributes")) {
    if (!isObject(kv) || typeof kv.key !== "string" || !ALLOWED_ATTRIBUTES.has(kv.key)) continue;
    const value = cleanValue(kv.key, kv.value, ctx);
    if (value) out.push({ key: kv.key, value });
  }
  return out;
}

function cleanStatus(status: unknown): Json | undefined {
  if (!isObject(status)) return undefined;
  const code = status.code;
  if (code !== 0 && code !== 1 && code !== 2) return undefined;
  const out: Json = { code };
  if (typeof status.message === "string" && status.message) out.message = status.message.slice(0, MAX_STRING);
  return out;
}

function cleanSpan(span: unknown, ctx: ScrubContext): Json {
  if (!isObject(span)) invalid("span");
  const traceId = optionalId(span.traceId, TRACE_ID, "traceId");
  const spanId = optionalId(span.spanId, SPAN_ID, "spanId");
  if (!traceId || !spanId) invalid("span ids");
  const out: Json = {
    traceId,
    spanId,
    name: name(span.name, "span name"),
    kind: typeof span.kind === "number" && span.kind >= 0 && span.kind <= 5 ? span.kind : 0,
    startTimeUnixNano: nanos(span.startTimeUnixNano, "startTimeUnixNano"),
    endTimeUnixNano: nanos(span.endTimeUnixNano, "endTimeUnixNano"),
    attributes: cleanAttributes(span.attributes, ctx),
  };
  const parent = optionalId(span.parentSpanId, SPAN_ID, "parentSpanId");
  if (parent) out.parentSpanId = parent;
  if (typeof span.flags === "number" && Number.isSafeInteger(span.flags)) out.flags = span.flags;
  const events = array(span.events, "events");
  if (events.length > MAX_EVENTS_PER_SPAN) invalid("too many events");
  out.events = events.map((e) => {
    if (!isObject(e)) invalid("event");
    return {
      timeUnixNano: nanos(e.timeUnixNano, "event time"),
      name: name(e.name, "event name"),
      attributes: cleanAttributes(e.attributes, ctx),
    };
  });
  const status = cleanStatus(span.status);
  if (status) out.status = status;
  return out;
}

function cleanLogRecord(record: unknown, ctx: ScrubContext): Json {
  if (!isObject(record)) invalid("log record");
  const out: Json = { attributes: cleanAttributes(record.attributes, ctx) };
  if (record.timeUnixNano !== undefined) out.timeUnixNano = nanos(record.timeUnixNano, "timeUnixNano");
  if (record.observedTimeUnixNano !== undefined) {
    out.observedTimeUnixNano = nanos(record.observedTimeUnixNano, "observedTimeUnixNano");
  }
  const severity = record.severityNumber;
  if (typeof severity === "number" && Number.isInteger(severity) && severity >= 0 && severity <= 24) {
    out.severityNumber = severity;
  }
  if (typeof record.severityText === "string" && /^[A-Z0-9]{1,8}$/.test(record.severityText)) {
    out.severityText = record.severityText;
  }
  if (record.eventName !== undefined) out.eventName = name(record.eventName, "eventName");
  const body = cleanValue("body", record.body, ctx);
  if (body && "stringValue" in body) out.body = body;
  const traceId = optionalId(record.traceId, TRACE_ID, "traceId");
  const spanId = optionalId(record.spanId, SPAN_ID, "spanId");
  if (traceId && spanId) Object.assign(out, { traceId, spanId });
  if (typeof record.flags === "number" && Number.isSafeInteger(record.flags)) out.flags = record.flags;
  return out;
}

function cleanScope(scope: unknown): Json {
  const out: Json = {};
  if (!isObject(scope)) return out;
  if (typeof scope.name === "string") out.name = scope.name.slice(0, 128);
  if (typeof scope.version === "string") out.version = scope.version.slice(0, 32);
  return out;
}

/** The resource every relayed record is given: ours, never the browser's. */
export function browserResource(env: Env): { attributes: KeyValue[] } {
  const attributes: KeyValue[] = [{ key: "service.name", value: { stringValue: BROWSER_SERVICE_NAME } }];
  if (env.VERCEL_GIT_COMMIT_SHA) {
    attributes.push({ key: "service.version", value: { stringValue: env.VERCEL_GIT_COMMIT_SHA } });
  }
  if (env.VERCEL_ENV) {
    attributes.push({ key: "deployment.environment.name", value: { stringValue: env.VERCEL_ENV } });
  }
  return { attributes };
}

const LAYOUT = {
  traces: { resources: "resourceSpans", scopes: "scopeSpans", items: "spans", clean: cleanSpan },
  logs: { resources: "resourceLogs", scopes: "scopeLogs", items: "logRecords", clean: cleanLogRecord },
} as const;

/**
 * Parses an OTLP/HTTP JSON export request for `signal` and rebuilds it from the allowlist: known
 * fields only, allowlisted attributes (URLs reduced to route patterns, with the API's origin from
 * NEXT_PUBLIC_API_URL), our resource, and at most MAX_ITEMS spans or records. Anything that isn't
 * that shape is a 400.
 */
export function sanitize(signal: RelaySignal, body: string, env: Env): Json {
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    invalid("JSON");
  }
  const layout = LAYOUT[signal];
  if (!isObject(parsed) || !Array.isArray(parsed[layout.resources])) invalid(layout.resources);

  const resource = browserResource(env);
  const ctx: ScrubContext = { apiOrigin: apiOriginOf(env.NEXT_PUBLIC_API_URL) };
  let count = 0;
  const resources = (parsed[layout.resources] as unknown[]).map((rs) => {
    if (!isObject(rs)) invalid(layout.resources);
    const scopes = array(rs[layout.scopes], layout.scopes).map((ss) => {
      if (!isObject(ss)) invalid(layout.scopes);
      const items = array(ss[layout.items], layout.items);
      count += items.length;
      if (count > MAX_ITEMS) throw new RelayError(413, `more than ${MAX_ITEMS} ${layout.items}`);
      return { scope: cleanScope(ss.scope), [layout.items]: items.map((item) => layout.clean(item, ctx)) };
    });
    return { resource, [layout.scopes]: scopes };
  });
  if (count === 0) invalid(`no ${layout.items}`);
  return { [layout.resources]: resources };
}

/** Where to forward a signal, and the headers to send, or undefined when it isn't exported. */
export interface ForwardTarget {
  url: string;
  headers: Record<string, string>;
}

/**
 * Parses an OTEL_EXPORTER_OTLP_HEADERS value: comma-separated `key=value` pairs, percent-encoded
 * (`Authorization=Bearer%20...`).
 */
export function parseOtlpHeaders(value: string | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  for (const pair of (value ?? "").split(",")) {
    const eq = pair.indexOf("=");
    if (eq <= 0) continue;
    try {
      out[decodeURIComponent(pair.slice(0, eq).trim())] = decodeURIComponent(pair.slice(eq + 1).trim());
    } catch {
      // A malformed pair is skipped, as the OTel SDKs do.
    }
  }
  return out;
}

/**
 * The Collector endpoint and headers for `signal`, read from the same standard variables as the
 * server's own exporters: OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT as is, or OTEL_EXPORTER_OTLP_ENDPOINT
 * plus `/v1/<signal>`, with OTEL_EXPORTER_OTLP_HEADERS and the signal's own headers on top.
 * Undefined when the server exports nothing for that signal (local dev, CI).
 */
export function forwardTarget(env: Env, signal: RelaySignal): ForwardTarget | undefined {
  const upper = signal === "traces" ? "TRACES" : "LOGS";
  if (!enabledSignals(env)[signal]) return undefined;
  const own = env[`OTEL_EXPORTER_OTLP_${upper}_ENDPOINT`];
  const url = own || `${(env.OTEL_EXPORTER_OTLP_ENDPOINT ?? "").replace(/\/+$/, "")}/v1/${signal}`;
  const headers = {
    ...parseOtlpHeaders(env.OTEL_EXPORTER_OTLP_HEADERS),
    ...parseOtlpHeaders(env[`OTEL_EXPORTER_OTLP_${upper}_HEADERS`]),
    "Content-Type": "application/json",
  };
  return { url, headers };
}

/**
 * Posts a sanitized body to the Collector. Only our headers go along; `opentelemetry.ignore` keeps
 * the server's fetch instrumentation from tracing the relay's own traffic. Returns whether the
 * Collector accepted it.
 */
export async function forward(target: ForwardTarget, body: Json): Promise<boolean> {
  const init: RequestInit & { opentelemetry?: { ignore: boolean } } = {
    method: "POST",
    headers: target.headers,
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(FORWARD_TIMEOUT_MS),
    opentelemetry: { ignore: true },
  };
  try {
    const res = await fetch(target.url, init);
    return res.ok;
  } catch {
    return false;
  }
}

/**
 * Handles one relay request end to end. Answers 404 for an unknown signal, 429 when the instance
 * is shedding load, 413/415/400 for a body it won't take, and 200 otherwise: also when there's no
 * Collector (the body is dropped) or the Collector failed, so browsers don't retry into an outage.
 */
export async function relay(
  request: Request,
  segment: string,
  shedder: LoadShedder,
  env: Env = process.env
): Promise<Response> {
  const signal = parseSignal(segment);
  if (!signal) return Response.json({ error: "not found" }, { status: 404 });
  if (!shedder.allow()) {
    return Response.json({ error: "busy" }, { status: 429, headers: { "Retry-After": "60" } });
  }
  let body: Json;
  try {
    body = sanitize(signal, await readRelayBody(request), env);
  } catch (err) {
    if (err instanceof RelayError) return Response.json({ error: err.message }, { status: err.status });
    throw err;
  }
  const target = forwardTarget(env, signal);
  if (target && !(await forward(target, body))) {
    console.warn(`obs relay: the Collector refused or didn't answer a ${signal} export`);
  }
  return Response.json({});
}
