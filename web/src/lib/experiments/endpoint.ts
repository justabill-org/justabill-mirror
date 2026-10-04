// POST /api/experiments: counts one exposure or conversion per visitor and experiment
// (docs/design/580-ab-experiments.md, option 3A). The arm comes from the visitor's jab_exp cookie,
// never from the body, and an event for no live experiment is dropped. Like the telemetry relay it's
// public and unauthenticated: it reads at most 256 bytes, caps requests per instance, logs nothing
// per request, and never reads the account.

import type { NextRequest } from "next/server";
import { userAgent } from "next/server";
import { instrument } from "@/lib/obs";
import {
  ATTR_EXPERIMENT_ID,
  ATTR_EXPERIMENT_VARIANT,
  METRIC_EXPERIMENT_CONVERSIONS,
  METRIC_EXPERIMENT_CONVERSIONS_DESCRIPTION,
  METRIC_EXPERIMENT_CONVERSIONS_UNIT,
  METRIC_EXPERIMENT_EXPOSURES,
  METRIC_EXPERIMENT_EXPOSURES_DESCRIPTION,
  METRIC_EXPERIMENT_EXPOSURES_UNIT,
} from "@/lib/obs/names";
import { LoadShedder } from "@/lib/obs/relay";
import { cookieVariant } from "./assign";
import { COOKIE_NAME, EXPERIMENTS, experimentsNow, findExperiment, isLive, type Experiment } from "./registry";

/** The largest body the endpoint reads. */
export const MAX_EVENT_BYTES = 256;

/** The two events a browser can send. */
export const EXPERIMENT_EVENTS = ["exposure", "conversion"] as const;

/** An event a browser sends. */
export type ExperimentEvent = (typeof EXPERIMENT_EVENTS)[number];

/** The body of POST /api/experiments. */
export interface EventBody {
  experiment: string;
  event: ExperimentEvent;
}

const METRICS = {
  exposure: {
    name: METRIC_EXPERIMENT_EXPOSURES,
    unit: METRIC_EXPERIMENT_EXPOSURES_UNIT,
    description: METRIC_EXPERIMENT_EXPOSURES_DESCRIPTION,
  },
  conversion: {
    name: METRIC_EXPERIMENT_CONVERSIONS,
    unit: METRIC_EXPERIMENT_CONVERSIONS_UNIT,
    description: METRIC_EXPERIMENT_CONVERSIONS_DESCRIPTION,
  },
} as const;

const NO_STORE = { "Cache-Control": "no-store" };

function status(code: number, headers: Record<string, string> = {}): Response {
  return new Response(null, { status: code, headers: { ...NO_STORE, ...headers } });
}

/** Reads the body as text, or returns undefined when it's over MAX_EVENT_BYTES (without reading the rest). */
async function readSmallBody(request: Request): Promise<string | undefined> {
  if (Number(request.headers.get("content-length") ?? "0") > MAX_EVENT_BYTES) return undefined;
  if (!request.body) return "";
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > MAX_EVENT_BYTES) {
      await reader.cancel();
      return undefined;
    }
    chunks.push(value);
  }
  return new TextDecoder().decode(Buffer.concat(chunks));
}

/** The body as an EventBody: exactly two known string fields, or undefined. */
export function parseEventBody(text: string): EventBody | undefined {
  let body: unknown;
  try {
    body = JSON.parse(text);
  } catch {
    return undefined;
  }
  if (typeof body !== "object" || body === null || Array.isArray(body)) return undefined;
  const fields = body as Record<string, unknown>;
  const keys = Object.keys(fields).sort();
  if (keys.length !== 2 || keys[0] !== "event" || keys[1] !== "experiment") return undefined;
  const { experiment, event } = fields;
  if (typeof experiment !== "string" || !EXPERIMENT_EVENTS.some((e) => e === event)) return undefined;
  return { experiment, event: event as ExperimentEvent };
}

/** What recordExperimentEvent needs; tests replace them. */
export interface EndpointOptions {
  experiments?: readonly Experiment[];
  now?: Date;
}

/**
 * Handles one POST /api/experiments: 429 when the instance's minute is full, 413 for a body over
 * 256 bytes, 400 for one that isn't `{"experiment", "event"}`, and otherwise 204, whether the event
 * was counted or dropped (no cookie, a cookie for another experiment, an experiment that isn't
 * live, a crawler or a GPC browser), so the answer says nothing about the visitor's arm.
 */
export async function recordExperimentEvent(
  request: NextRequest,
  shedder: LoadShedder,
  opts: EndpointOptions = {}
): Promise<Response> {
  if (!shedder.allow()) return status(429, { "Retry-After": "60" });
  const text = await readSmallBody(request);
  if (text === undefined) return status(413);
  const body = parseEventBody(text);
  if (!body) return status(400);

  const experiment = findExperiment(body.experiment, opts.experiments ?? EXPERIMENTS);
  if (!experiment || !isLive(experiment, opts.now ?? experimentsNow())) return status(204);
  if (request.headers.get("sec-gpc") === "1" || userAgent(request).isBot) return status(204);
  const variant = cookieVariant(request.cookies.get(COOKIE_NAME)?.value, experiment);
  if (!variant) return status(204);

  const metric = METRICS[body.event];
  const counter = instrument(metric.name, (meter) =>
    meter.createCounter(metric.name, { unit: metric.unit, description: metric.description })
  );
  counter.add(1, { [ATTR_EXPERIMENT_ID]: experiment.id, [ATTR_EXPERIMENT_VARIANT]: variant });
  return status(204);
}
