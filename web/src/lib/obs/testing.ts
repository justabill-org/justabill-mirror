// In-memory telemetry for Vitest (design docs/design/53-observability.md, "Testable"), the
// counterpart of the Go obs/obstest package. Tests only: it replaces the global providers.

import { context, metrics, propagation, trace } from "@opentelemetry/api";
import { logs } from "@opentelemetry/api-logs";
import { AsyncLocalStorageContextManager } from "@opentelemetry/context-async-hooks";
import { W3CTraceContextPropagator } from "@opentelemetry/core";
import {
  InMemoryLogRecordExporter,
  LoggerProvider,
  SimpleLogRecordProcessor,
  type ReadableLogRecord,
} from "@opentelemetry/sdk-logs";
import {
  AggregationTemporality,
  InMemoryMetricExporter,
  MeterProvider,
  PeriodicExportingMetricReader,
  type DataPoint,
  type MetricData,
} from "@opentelemetry/sdk-metrics";
import {
  BasicTracerProvider,
  InMemorySpanExporter,
  SimpleSpanProcessor,
  type ReadableSpan,
} from "@opentelemetry/sdk-trace-base";

/** Telemetry recorded in memory by {@link setupTestTelemetry}. */
export interface TestTelemetry {
  /** The spans ended so far. */
  spans(): ReadableSpan[];
  /** The log records emitted so far. */
  logs(): ReadableLogRecord[];
  /** Collects the metrics and returns the metric called `name`, if it has been recorded. */
  metric(name: string): Promise<MetricData | undefined>;
  /** Collects the metrics and returns the data points of the metric called `name`. */
  points(name: string): Promise<DataPoint<unknown>[]>;
  /** Clears recorded spans and logs and restores the no-op global providers. */
  shutdown(): Promise<void>;
}

/**
 * Installs global tracer, meter and logger providers that record in memory, plus W3C trace
 * context propagation and an AsyncLocalStorage context manager. Call it before the code under
 * test runs, and `shutdown` it after each test.
 */
export function setupTestTelemetry(): TestTelemetry {
  trace.disable();
  metrics.disable();
  logs.disable();
  propagation.disable();
  context.disable();

  const spanExporter = new InMemorySpanExporter();
  const tracerProvider = new BasicTracerProvider({ spanProcessors: [new SimpleSpanProcessor(spanExporter)] });
  const metricExporter = new InMemoryMetricExporter(AggregationTemporality.CUMULATIVE);
  const metricReader = new PeriodicExportingMetricReader({ exporter: metricExporter, exportIntervalMillis: 60_000 });
  const meterProvider = new MeterProvider({ readers: [metricReader] });
  const logExporter = new InMemoryLogRecordExporter();
  const loggerProvider = new LoggerProvider({ processors: [new SimpleLogRecordProcessor({ exporter: logExporter })] });

  context.setGlobalContextManager(new AsyncLocalStorageContextManager().enable());
  propagation.setGlobalPropagator(new W3CTraceContextPropagator());
  trace.setGlobalTracerProvider(tracerProvider);
  metrics.setGlobalMeterProvider(meterProvider);
  logs.setGlobalLoggerProvider(loggerProvider);

  const metric = async (name: string) => {
    metricExporter.reset();
    await metricReader.forceFlush();
    const found = metricExporter
      .getMetrics()
      .flatMap((rm) => rm.scopeMetrics.flatMap((sm) => sm.metrics))
      .filter((m) => m.descriptor.name === name);
    return found.at(-1);
  };

  return {
    spans: () => spanExporter.getFinishedSpans(),
    logs: () => logExporter.getFinishedLogRecords(),
    metric,
    points: async (name) => ((await metric(name))?.dataPoints ?? []) as DataPoint<unknown>[],
    shutdown: async () => {
      spanExporter.reset();
      logExporter.reset();
      await Promise.all([tracerProvider.shutdown(), meterProvider.shutdown(), loggerProvider.shutdown()]);
      trace.disable();
      metrics.disable();
      logs.disable();
      propagation.disable();
      context.disable();
    },
  };
}
