// POST /api/otel/v1/{traces,logs}: the same-origin relay for browser telemetry.
// Design: docs/design/53-observability.md, Decision 5. The checks live in lib/obs/relay.ts.

import { LoadShedder, relay } from "@/lib/obs/relay";

// One per server instance: above 100 requests a minute it answers 429 (until #51's WAF rule).
const shedder = new LoadShedder();

export async function POST(request: Request, ctx: { params: Promise<{ signal: string }> }): Promise<Response> {
  const { signal } = await ctx.params;
  return relay(request, signal, shedder);
}
