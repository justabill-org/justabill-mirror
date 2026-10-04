// The live experiment's proxy (docs/design/580-ab-experiments.md, option 1B): it puts each visitor
// to /vote in an arm, keeps the arm in the jab_exp cookie, and rewrites treatment to
// /vote/v/treatment. This file exists only while lib/experiments/registry.ts has an experiment:
// the experiment's end PR deletes it, and experiments-registry.test.ts fails if the two disagree.
// The matcher must be a constant, equal to matcherFor() of each experiment's path.

import type { NextRequest, NextResponse } from "next/server";
import { experimentResponse } from "@/lib/experiments/assign";

export function proxy(request: NextRequest): NextResponse {
  return experimentResponse(request);
}

export const config = {
  // The A/A run on /vote (#695).
  matcher: ["/vote"],
};
