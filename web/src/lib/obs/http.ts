// Client spans and metrics for calls to our own API (lib/api.ts). These are hand-made rather than
// @vercel/otel's fetch instrumentation so that every failed response, 4xx included, marks the span
// as an error (HTTP semconv for CLIENT spans) and lands on http.client.request.duration with its
// error.type. The instrumentation is told to skip these calls so there's one span per hop.

import { context, propagation, SpanKind, SpanStatusCode, trace, type Attributes, type Histogram } from "@opentelemetry/api";
import {
  ATTR_ERROR_TYPE,
  ATTR_HTTP_REQUEST_METHOD,
  ATTR_HTTP_RESPONSE_STATUS_CODE,
  ATTR_SERVER_ADDRESS,
  ATTR_SERVER_PORT,
  ATTR_URL_FULL,
  METRIC_HTTP_CLIENT_REQUEST_DURATION,
} from "@opentelemetry/semantic-conventions";

import { errorType, failSpan, instrument, tracer } from "./index";

/** The HTTP semconv's recommended buckets for the request duration histograms, in seconds. */
export const DURATION_BUCKETS = [0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10];

const DEFAULT_PORTS: Record<string, number> = { "http:": 80, "https:": 443 };

function durationHistogram(): Histogram {
  return instrument(METRIC_HTTP_CLIENT_REQUEST_DURATION, (meter) =>
    meter.createHistogram(METRIC_HTTP_CLIENT_REQUEST_DURATION, {
      unit: "s",
      description: "Duration of HTTP client requests.",
      advice: { explicitBucketBoundaries: DURATION_BUCKETS },
    })
  );
}

/**
 * Fetches `url` from our API inside a CLIENT span named for the method, and records
 * http.client.request.duration, and sends the trace context (`traceparent`), which only ever goes
 * to the API because only lib/api.ts calls this, on the server. A response that isn't ok sets the
 * span's status to error and `error.type` to the status code; a network failure sets it to the
 * error's class and is rethrown. The query string is never recorded: it can hold an address.
 */
export async function tracedFetch(url: string, init: RequestInit = {}): Promise<Response> {
  const method = (init.method ?? "GET").toUpperCase();
  const target = new URL(url);
  const port = target.port ? Number(target.port) : DEFAULT_PORTS[target.protocol];
  const attrs: Attributes = {
    [ATTR_HTTP_REQUEST_METHOD]: method,
    [ATTR_SERVER_ADDRESS]: target.hostname,
    ...(port ? { [ATTR_SERVER_PORT]: port } : {}),
  };
  const span = tracer().startSpan(method, {
    kind: SpanKind.CLIENT,
    attributes: { ...attrs, [ATTR_URL_FULL]: `${target.origin}${target.pathname}` },
  });

  const headers = headerRecord(init.headers);
  propagation.inject(trace.setSpan(context.active(), span), headers);

  const start = performance.now();
  const record = (extra: Attributes) =>
    durationHistogram().record((performance.now() - start) / 1000, { ...attrs, ...extra });

  let res: Response;
  try {
    // `opentelemetry.ignore` keeps @vercel/otel's fetch instrumentation from adding a second span.
    const opts: RequestInit & { opentelemetry?: { ignore: boolean } } = {
      ...init,
      headers,
      opentelemetry: { ignore: true },
    };
    res = await fetch(url, opts);
  } catch (err) {
    failSpan(span, err);
    span.setAttribute(ATTR_ERROR_TYPE, errorType(err));
    record({ [ATTR_ERROR_TYPE]: errorType(err) });
    span.end();
    throw err;
  }

  const status: Attributes = { [ATTR_HTTP_RESPONSE_STATUS_CODE]: res.status };
  if (!res.ok) {
    status[ATTR_ERROR_TYPE] = String(res.status);
    span.setStatus({ code: SpanStatusCode.ERROR, message: `api_error ${res.status}` });
  }
  span.setAttributes(status);
  record(status);
  span.end();
  return res;
}

/**
 * Copies request headers into a record that traceparent can be injected into. A record keeps its
 * keys as given; a Headers object or a list of pairs (which spreading would lose) is converted.
 */
function headerRecord(init: HeadersInit | undefined): Record<string, string> {
  if (init instanceof Headers || Array.isArray(init)) return Object.fromEntries(new Headers(init));
  return { ...init };
}
