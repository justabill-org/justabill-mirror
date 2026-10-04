// The OpenTelemetry Web SDK setup for the browser (design docs/design/53-observability.md,
// Decision 5). browser.ts loads this module with a dynamic import after the page has loaded, so
// its ~40 KB of SDK never delays first paint; nothing else may import it statically.

import { logs } from "@opentelemetry/api-logs";
import { OTLPLogExporter } from "@opentelemetry/exporter-logs-otlp-http";
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-http";
import { registerInstrumentations } from "@opentelemetry/instrumentation";
import { DocumentLoadInstrumentation } from "@opentelemetry/instrumentation-document-load";
import { FetchInstrumentation } from "@opentelemetry/instrumentation-fetch";
import { resourceFromAttributes } from "@opentelemetry/resources";
import { BatchLogRecordProcessor, LoggerProvider } from "@opentelemetry/sdk-logs";
import {
  BatchSpanProcessor,
  ParentBasedSampler,
  TraceIdRatioBasedSampler,
  WebTracerProvider,
  type ReadableSpan,
  type Span,
  type SpanProcessor,
} from "@opentelemetry/sdk-trace-web";
import type { Context } from "@opentelemetry/api";

import { logException, withSpan } from "./index";
import { reduceUrl } from "./relay-url";

export { reduceUrl } from "./relay-url";

/** The relay's paths on our own origin (src/app/api/otel/v1/[signal]/route.ts). */
export const RELAY_PATH = "/api/otel/v1";

// Spans per export. The relay takes 64 KB, and a fetch span with its timing events is ~1.5 KB.
const MAX_EXPORT_BATCH = 32;

/** What the browser SDK needs from the page. */
export interface BrowserSdkConfig {
  /** Our own origin (`location.origin`), where the relay is. */
  origin: string;
  /** The API's origin: the only other origin that gets `traceparent`. */
  apiOrigin: string;
  /** The share of new traces kept, 0 to 1. */
  sampleRatio: number;
}

/** The loaded SDK, as browser.ts uses it. */
export interface BrowserSdk {
  withSpan: typeof withSpan;
  logException: typeof logException;
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * Scrubs each span before the exporting processor sees it: drops the user agent, reduces
 * `url.full` to a route pattern with {@link reduceUrl} (or drops it when it isn't an http(s) URL),
 * and leaves out `resourceFetch` spans (one per script, style and image: noise, and too many for
 * the relay's size cap).
 */
export class BrowserSpanProcessor implements SpanProcessor {
  constructor(
    private readonly next: SpanProcessor,
    private readonly origin: string,
    private readonly apiOrigin: string
  ) {}

  onStart(span: Span, parent: Context): void {
    this.next.onStart(span, parent);
  }

  onEnd(span: ReadableSpan): void {
    if (span.name === "resourceFetch") return;
    const attrs = span.attributes;
    delete attrs["user_agent.original"];
    const url = attrs["url.full"];
    if (url !== undefined) {
      const reduced = typeof url === "string" ? reduceUrl(url, this.origin, this.apiOrigin) : undefined;
      if (reduced === undefined) delete attrs["url.full"];
      else attrs["url.full"] = reduced;
    }
    this.next.onEnd(span);
  }

  forceFlush(): Promise<void> {
    return this.next.forceFlush();
  }

  shutdown(): Promise<void> {
    return this.next.shutdown();
  }
}

/**
 * Registers the browser's tracer and logger providers, exporting OTLP/HTTP JSON to the relay, with
 * document-load and fetch instrumentation. `traceparent` goes to our origin and the API origin
 * only. Batches are flushed when the page is hidden. Returns the helpers browser.ts calls.
 */
export function setupBrowserSdk(config: BrowserSdkConfig): BrowserSdk {
  const resource = resourceFromAttributes({ "service.name": "justabill-browser" });
  const spanExporter = new OTLPTraceExporter({ url: `${config.origin}${RELAY_PATH}/traces` });
  const tracerProvider = new WebTracerProvider({
    resource,
    sampler: new ParentBasedSampler({ root: new TraceIdRatioBasedSampler(config.sampleRatio) }),
    spanProcessors: [
      new BrowserSpanProcessor(
        new BatchSpanProcessor(spanExporter, { maxExportBatchSize: MAX_EXPORT_BATCH }),
        config.origin,
        config.apiOrigin
      ),
    ],
  });
  tracerProvider.register();

  const logExporter = new OTLPLogExporter({ url: `${config.origin}${RELAY_PATH}/logs` });
  logs.setGlobalLoggerProvider(
    new LoggerProvider({ resource, processors: [new BatchLogRecordProcessor({ exporter: logExporter })] })
  );

  registerInstrumentations({
    tracerProvider,
    instrumentations: [
      new DocumentLoadInstrumentation(),
      new FetchInstrumentation({
        propagateTraceHeaderCorsUrls: [new RegExp(`^${escapeRegExp(config.apiOrigin)}/`)],
        ignoreUrls: [new RegExp(`^${escapeRegExp(config.origin)}${RELAY_PATH}/`)],
        clearTimingResources: true,
      }),
    ],
  });

  return { withSpan, logException };
}
