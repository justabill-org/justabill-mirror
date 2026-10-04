#!/usr/bin/env node
// An experiment's readout (docs/design/580-ab-experiments.md, option 4A): run once, at the end, on
// the four totals from the dashboard or PromQL, and post its output on the experiment's issue.
//
//   npm run experiment:readout -- <control exposures> <control conversions> \
//     <treatment exposures> <treatment conversions>
//
// It runs src/lib/experiments/stats.ts with Node's type stripping (Node 24), so the readout uses
// the same tested code as the registry's sample sizes.

import { formatReadout } from "../src/lib/experiments/stats.ts";

const USAGE =
  "usage: experiment-readout <control exposures> <control conversions> <treatment exposures> <treatment conversions>";

const args = process.argv.slice(2);
const numbers = args.map((a) => (/^\d+$/.test(a) ? Number(a) : Number.NaN));
if (numbers.length !== 4 || numbers.some(Number.isNaN)) {
  console.error(USAGE);
  process.exit(2);
}
const [ce, cc, te, tc] = numbers;
try {
  console.log(
    formatReadout({ control: { exposures: ce, conversions: cc }, treatment: { exposures: te, conversions: tc } })
  );
} catch (err) {
  console.error(`experiment-readout: ${err instanceof Error ? err.message : String(err)}`);
  process.exit(1);
}
