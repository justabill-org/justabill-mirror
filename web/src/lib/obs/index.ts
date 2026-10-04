// The web app's observability helpers (design docs/design/53-observability.md, Decision 3). They
// use only the OpenTelemetry API, so they're safe in server and browser code alike: until
// server.ts registers the SDK (or when it stays off), every call here is a no-op. Only lib/obs
// imports SDK or exporter packages; ESLint's no-restricted-imports enforces that.

import {
  metrics,
  SpanStatusCode,
  trace,
  type Attributes,
  type Meter,
  type MeterProvider,
  type Span,
  type Tracer,
} from "@opentelemetry/api";
import { logs, SeverityNumber, type Logger } from "@opentelemetry/api-logs";
import {
  ATTR_EXCEPTION_MESSAGE,
  ATTR_EXCEPTION_STACKTRACE,
  ATTR_EXCEPTION_TYPE,
  ATTR_HTTP_ROUTE,
} from "@opentelemetry/semantic-conventions";

/** The instrumentation scope for the web app's own spans, metrics and logs. */
export const SCOPE = "justabill-web";

/** The tracer for the web app's own spans. */
export function tracer(): Tracer {
  return trace.getTracer(SCOPE);
}

/** The logger for the web app's own log records (the OTel logs API; a no-op until registered). */
export function logger(): Logger {
  return logs.getLogger(SCOPE);
}

// The global MeterProvider isn't a proxy: a meter taken before the SDK registers stays a no-op
// forever. So the meter is looked up again whenever the global provider changes.
let cachedMeter: { provider: MeterProvider; meter: Meter; instruments: Map<string, unknown> } | undefined;

/**
 * Returns the instrument called `name`, creating it with `create` the first time it's asked for on
 * the current global MeterProvider.
 */
export function instrument<T>(name: string, create: (meter: Meter) => T): T {
  const provider = metrics.getMeterProvider();
  if (cachedMeter?.provider !== provider) {
    cachedMeter = { provider, meter: provider.getMeter(SCOPE), instruments: new Map() };
  }
  let inst = cachedMeter.instruments.get(name) as T | undefined;
  if (inst === undefined) {
    inst = create(cachedMeter.meter);
    cachedMeter.instruments.set(name, inst);
  }
  return inst;
}

/** The error's class name, for `error.type` and `exception.type`. */
export function errorType(err: unknown): string {
  if (err instanceof Error) return err.name || err.constructor.name;
  return typeof err;
}

/** Records `err` on `span` as an exception event and sets the span's status to error. */
export function failSpan(span: Span, err: unknown): void {
  if (err instanceof Error) {
    span.recordException(err);
  } else {
    span.recordException(String(err));
  }
  span.setStatus({ code: SpanStatusCode.ERROR, message: err instanceof Error ? err.message : String(err) });
}

/**
 * Runs `fn` inside an INTERNAL span called `name`, for a business operation (render a page,
 * compare votes). A thrown error or rejection marks the span as failed and is rethrown. Never put
 * user data (addresses, votes, emails) in `attributes`.
 */
export async function withSpan<T>(
  name: string,
  fn: (span: Span) => T | Promise<T>,
  attributes?: Attributes
): Promise<T> {
  return tracer().startActiveSpan(name, { attributes }, async (span) => {
    try {
      return await fn(span);
    } catch (err) {
      failSpan(span, err);
      throw err;
    } finally {
      span.end();
    }
  });
}

/**
 * Writes an ERROR `exception` log record for `err` with the route pattern (`/bills/[id]`), never
 * the concrete path. It carries the active trace, so the log line links to its request's trace.
 */
export function logException(err: unknown, route?: string): void {
  const attributes: Record<string, string> = { [ATTR_EXCEPTION_TYPE]: errorType(err) };
  attributes[ATTR_EXCEPTION_MESSAGE] = err instanceof Error ? err.message : String(err);
  if (err instanceof Error && err.stack) attributes[ATTR_EXCEPTION_STACKTRACE] = err.stack;
  if (route) attributes[ATTR_HTTP_ROUTE] = route;
  logger().emit({
    eventName: "exception",
    severityNumber: SeverityNumber.ERROR,
    severityText: "ERROR",
    body: attributes[ATTR_EXCEPTION_MESSAGE],
    attributes,
  });
}

/**
 * Exports buffered log records now, so a record written just before a Vercel function is frozen
 * isn't lost. A no-op while the SDK isn't registered.
 */
export async function flushLogs(): Promise<void> {
  const provider = logs.getLoggerProvider() as { forceFlush?: () => Promise<void> };
  try {
    await provider.forceFlush?.();
  } catch {
    // Telemetry export failures must never fail the request.
  }
}
