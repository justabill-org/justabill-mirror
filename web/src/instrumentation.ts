// Next.js calls register() once per server instance and onRequestError for every uncaught server
// error (docs/design/53-observability.md, item 6). Telemetry is set up in lib/obs/server.ts.

import type { Instrumentation } from "next";

import { flushLogs, logException } from "@/lib/obs";

export async function register(): Promise<void> {
  // The obs SDK setup and its OTLP exporters are Node.js only; the app has no Edge routes.
  if (process.env.NEXT_RUNTIME === "nodejs") {
    const { registerObs } = await import("@/lib/obs/server");
    registerObs();
  }
}

/**
 * Writes an `exception` log record with the route pattern (`/bills/[id]`), never the request's
 * path, query or headers, and flushes it before the function can be frozen.
 */
export const onRequestError: Instrumentation.onRequestError = async (error, _request, errorContext) => {
  logException(error, errorContext.routePath);
  await flushLogs();
};
