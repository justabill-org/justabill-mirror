// POST /api/experiments: one exposure or conversion per visitor and experiment, counted per arm.
// Design: docs/design/580-ab-experiments.md, option 3A. The checks live in lib/experiments/endpoint.ts.

import type { NextRequest } from "next/server";
import { recordExperimentEvent } from "@/lib/experiments/endpoint";
import { LoadShedder } from "@/lib/obs/relay";

// One per server instance. At most two events per visitor, so the cap sits above the relay's 100:
// a shed event is lost from both arms alike, so it shrinks the sample without biasing it.
const EVENTS_PER_MINUTE = 600;
const shedder = new LoadShedder(EVENTS_PER_MINUTE);

export async function POST(request: NextRequest): Promise<Response> {
  return recordExperimentEvent(request, shedder);
}
