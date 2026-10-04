// The statistics behind an experiment's sample size and its readout (docs/design/580-ab-experiments.md,
// option 4A: a fixed horizon). Pure functions with no imports, so `scripts/experiment-readout.mjs`
// can run this file with Node's type stripping: keep to erasable TypeScript (no enums, no
// parameter properties) and relative imports with a `.ts` extension, if any are ever needed.

/** The two-sided significance level every experiment uses. */
export const ALPHA = 0.05;

/** The power every experiment is sized for. */
export const POWER = 0.8;

/** A sample-ratio check's p-value below this means the counting is broken and the result is void. */
export const SRM_THRESHOLD = 0.001;

// Coefficients of Acklam's rational approximation to the normal quantile (relative error under
// 1.15e-9), from https://web.archive.org/web/20151030215612/http://home.online.no/~pjacklam/notes/invnorm/.
const A = [-3.969683028665376e1, 2.209460984245205e2, -2.759285104469687e2, 1.38357751867269e2,
  -3.066479806614716e1, 2.506628277459239];
const B = [-5.447609879822406e1, 1.615858368580409e2, -1.556989798598866e2, 6.680131188771972e1,
  -1.328068155288572e1];
const C = [-7.784894002430293e-3, -3.223964580411365e-1, -2.400758277161838, -2.549732539343734,
  4.374664141464968, 2.938163982698783];
const D = [7.784695709041462e-3, 3.224671290700398e-1, 2.445134137142996, 3.754408661907416];
const P_LOW = 0.02425;

/** The standard normal quantile: the z with Φ(z) = p, for 0 < p < 1. */
export function normalQuantile(p: number): number {
  if (!(p > 0 && p < 1)) throw new RangeError(`normalQuantile: p must be in (0, 1), got ${p}`);
  if (p < P_LOW) {
    const q = Math.sqrt(-2 * Math.log(p));
    return (((((C[0] * q + C[1]) * q + C[2]) * q + C[3]) * q + C[4]) * q + C[5]) /
      ((((D[0] * q + D[1]) * q + D[2]) * q + D[3]) * q + 1);
  }
  if (p > 1 - P_LOW) return -normalQuantile(1 - p);
  const q = p - 0.5;
  const r = q * q;
  return (((((A[0] * r + A[1]) * r + A[2]) * r + A[3]) * r + A[4]) * r + A[5]) * q /
    (((((B[0] * r + B[1]) * r + B[2]) * r + B[3]) * r + B[4]) * r + 1);
}

/**
 * The complementary error function, by the Chebyshev fit in Numerical Recipes (`erfcc`, fractional
 * error under 1.2e-7 everywhere).
 */
function erfc(x: number): number {
  const z = Math.abs(x);
  const t = 1 / (1 + 0.5 * z);
  const r = t * Math.exp(-z * z - 1.26551223 + t * (1.00002368 + t * (0.37409196 + t * (0.09678418 +
    t * (-0.18628806 + t * (0.27886807 + t * (-1.13520398 + t * (1.48851587 +
    t * (-0.82215223 + t * 0.17087277)))))))));
  return x >= 0 ? r : 2 - r;
}

/** The standard normal CDF, Φ(z). */
export function normalCdf(z: number): number {
  return 0.5 * erfc(-z / Math.SQRT2);
}

/** The two-sided p-value of a standard normal statistic. */
function twoSidedP(z: number): number {
  return Math.min(1, erfc(Math.abs(z) / Math.SQRT2));
}

/**
 * Visitors needed in each arm to detect a change in a conversion rate from `baseline` to `target`
 * with a two-sided two-proportion z-test at `alpha` and `power` (the pooled-variance formula, as
 * in Fleiss, Statistical Methods for Rates and Proportions, without continuity correction).
 * 10% → 12% needs 3,841; 20% → 23% needs 2,943.
 */
export function sampleSizePerArm(baseline: number, target: number, alpha = ALPHA, power = POWER): number {
  for (const [name, p] of [["baseline", baseline], ["target", target]] as const) {
    if (!(p > 0 && p < 1)) throw new RangeError(`sampleSizePerArm: ${name} must be in (0, 1), got ${p}`);
  }
  if (baseline === target) throw new RangeError("sampleSizePerArm: baseline and target must differ");
  const zAlpha = normalQuantile(1 - alpha / 2);
  const zBeta = normalQuantile(power);
  const mean = (baseline + target) / 2;
  const pooled = zAlpha * Math.sqrt(2 * mean * (1 - mean));
  const separate = zBeta * Math.sqrt(baseline * (1 - baseline) + target * (1 - target));
  return Math.ceil((pooled + separate) ** 2 / (baseline - target) ** 2);
}

/** One arm's totals: visitors exposed and visitors who converted. */
export interface ArmTotals {
  exposures: number;
  conversions: number;
}

/** The result of {@link twoProportionTest}. */
export interface ProportionTest {
  controlRate: number;
  treatmentRate: number;
  /** treatmentRate − controlRate, in absolute terms (0.02 is two percentage points). */
  lift: number;
  /** The 95% (or 1 − alpha) confidence interval of the lift, from the unpooled standard error. */
  liftInterval: [number, number];
  /** The lift relative to the control rate (0.2 is 20% better), or NaN when control is 0. */
  relativeLift: number;
  z: number;
  pValue: number;
  significant: boolean;
}

function checkTotals(name: string, arm: ArmTotals): void {
  const { exposures, conversions } = arm;
  if (!Number.isInteger(exposures) || exposures <= 0) {
    throw new RangeError(`${name}: exposures must be a positive whole number, got ${exposures}`);
  }
  if (!Number.isInteger(conversions) || conversions < 0) {
    throw new RangeError(`${name}: conversions must be a whole number, got ${conversions}`);
  }
  if (conversions > exposures) {
    throw new RangeError(`${name}: ${conversions} conversions is more than ${exposures} exposures: the counts are broken`);
  }
}

/** A two-sided two-proportion z-test (pooled standard error) of treatment against control. */
export function twoProportionTest(control: ArmTotals, treatment: ArmTotals, alpha = ALPHA): ProportionTest {
  checkTotals("control", control);
  checkTotals("treatment", treatment);
  const n1 = control.exposures;
  const n2 = treatment.exposures;
  const p1 = control.conversions / n1;
  const p2 = treatment.conversions / n2;
  const pooled = (control.conversions + treatment.conversions) / (n1 + n2);
  const sePooled = Math.sqrt(pooled * (1 - pooled) * (1 / n1 + 1 / n2));
  const lift = p2 - p1;
  const z = sePooled === 0 ? 0 : lift / sePooled;
  const pValue = twoSidedP(z);
  const seUnpooled = Math.sqrt((p1 * (1 - p1)) / n1 + (p2 * (1 - p2)) / n2);
  const margin = normalQuantile(1 - alpha / 2) * seUnpooled;
  return {
    controlRate: p1,
    treatmentRate: p2,
    lift,
    liftInterval: [lift - margin, lift + margin],
    relativeLift: p1 === 0 ? Number.NaN : lift / p1,
    z,
    pValue,
    significant: pValue < alpha,
  };
}

/** The result of {@link sampleRatioCheck}. */
export interface SampleRatioCheck {
  /** The share of exposures in treatment. */
  treatmentShare: number;
  chiSquare: number;
  pValue: number;
  /** True when pValue < SRM_THRESHOLD: the split isn't what assignment promised. */
  mismatch: boolean;
}

/**
 * A χ² goodness-of-fit test (one degree of freedom) of the exposure split against the expected
 * share in treatment (half, for our 50/50 assignment).
 */
export function sampleRatioCheck(controlExposures: number, treatmentExposures: number, expected = 0.5): SampleRatioCheck {
  const total = controlExposures + treatmentExposures;
  if (!(total > 0)) throw new RangeError("sampleRatioCheck: no exposures");
  const wantControl = total * (1 - expected);
  const wantTreatment = total * expected;
  const chiSquare = (controlExposures - wantControl) ** 2 / wantControl +
    (treatmentExposures - wantTreatment) ** 2 / wantTreatment;
  // With one degree of freedom, χ² is z², so its upper tail is the normal's two-sided tail.
  const pValue = twoSidedP(Math.sqrt(chiSquare));
  return { treatmentShare: treatmentExposures / total, chiSquare, pValue, mismatch: pValue < SRM_THRESHOLD };
}

/** The four totals a readout takes. */
export interface ReadoutTotals {
  control: ArmTotals;
  treatment: ArmTotals;
}

function pct(x: number, digits = 2): string {
  return `${(x * 100).toFixed(digits)}%`;
}

function points(x: number): string {
  const s = (x * 100).toFixed(2);
  return `${x >= 0 ? "+" : ""}${s} pp`;
}

/** The plain-text readout an experiment's end PR posts on its issue. */
export function formatReadout(totals: ReadoutTotals, alpha = ALPHA): string {
  const test = twoProportionTest(totals.control, totals.treatment, alpha);
  const srm = sampleRatioCheck(totals.control.exposures, totals.treatment.exposures);
  const confidence = Math.round((1 - alpha) * 100);
  const relative = Number.isNaN(test.relativeLift) ? "n/a" : `${test.relativeLift >= 0 ? "+" : ""}${pct(test.relativeLift, 1)}`;
  const lines = [
    `control:   ${totals.control.conversions} / ${totals.control.exposures} = ${pct(test.controlRate)}`,
    `treatment: ${totals.treatment.conversions} / ${totals.treatment.exposures} = ${pct(test.treatmentRate)}`,
    `lift: ${points(test.lift)} (${relative} relative), ${confidence}% interval ` +
      `[${points(test.liftInterval[0])}, ${points(test.liftInterval[1])}]`,
    `two-proportion z-test: z = ${test.z.toFixed(3)}, p = ${test.pValue.toPrecision(3)} ` +
      `(${test.significant ? "significant" : "not significant"} at α = ${alpha})`,
    `sample ratio: ${pct(srm.treatmentShare)} in treatment, χ² = ${srm.chiSquare.toFixed(3)}, ` +
      `p = ${srm.pValue.toPrecision(3)}`,
  ];
  if (srm.mismatch) {
    lines.push(`SAMPLE RATIO MISMATCH (p < ${SRM_THRESHOLD}): the counting is broken and this result is void.`);
  }
  return lines.join("\n");
}
