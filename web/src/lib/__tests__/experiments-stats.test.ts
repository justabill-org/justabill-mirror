import { spawnSync } from "node:child_process";
import path from "node:path";
import { describe, expect, it } from "vitest";

import {
  formatReadout,
  normalCdf,
  normalQuantile,
  sampleRatioCheck,
  sampleSizePerArm,
  twoProportionTest,
} from "../experiments/stats";

describe("normal distribution", () => {
  it.each([
    [0.975, 1.959964],
    [0.8, 0.841621],
    [0.5, 0],
    [0.01, -2.326348],
    [0.999, 3.090232],
  ])("normalQuantile(%f) = %f", (p, z) => {
    expect(normalQuantile(p)).toBeCloseTo(z, 5);
  });

  it("normalCdf inverts normalQuantile", () => {
    for (const p of [0.001, 0.05, 0.3, 0.5, 0.9, 0.995]) {
      expect(normalCdf(normalQuantile(p))).toBeCloseTo(p, 6);
    }
  });

  it("rejects p outside (0, 1)", () => {
    expect(() => normalQuantile(0)).toThrow(RangeError);
    expect(() => normalQuantile(1)).toThrow(RangeError);
  });
});

describe("sampleSizePerArm", () => {
  // The design's worked numbers (docs/design/580-ab-experiments.md, "Facts checked"), from the
  // pooled-variance formula at α = 0.05 two-sided and 80% power.
  it("needs 3,841 per arm for 10% → 12%", () => {
    expect(sampleSizePerArm(0.1, 0.12)).toBe(3841);
  });

  it("needs 2,943 per arm for 20% → 23%", () => {
    expect(sampleSizePerArm(0.2, 0.23)).toBe(2943);
  });

  it("is symmetric in the direction of the change", () => {
    expect(sampleSizePerArm(0.12, 0.1)).toBe(3841);
  });

  it("rejects rates outside (0, 1) and no change", () => {
    expect(() => sampleSizePerArm(0, 0.1)).toThrow(/baseline/);
    expect(() => sampleSizePerArm(0.1, 1)).toThrow(/target/);
    expect(() => sampleSizePerArm(0.1, 0.1)).toThrow(/differ/);
  });
});

describe("twoProportionTest", () => {
  it("matches a worked example: 200/1000 against 250/1000", () => {
    // Pooled p = 0.225, SE = 0.018675, z = 2.6774, two-sided p = 0.00742.
    const t = twoProportionTest({ exposures: 1000, conversions: 200 }, { exposures: 1000, conversions: 250 });
    expect(t.controlRate).toBe(0.2);
    expect(t.treatmentRate).toBe(0.25);
    expect(t.lift).toBeCloseTo(0.05, 10);
    expect(t.relativeLift).toBeCloseTo(0.25, 10);
    expect(t.z).toBeCloseTo(2.6774, 4);
    expect(t.pValue).toBeCloseTo(0.00742, 5);
    expect(t.significant).toBe(true);
    // Unpooled SE = sqrt(0.16/1000 + 0.1875/1000) = 0.018641; 1.96 × that = 0.036536.
    expect(t.liftInterval[0]).toBeCloseTo(0.013464, 5);
    expect(t.liftInterval[1]).toBeCloseTo(0.086536, 5);
  });

  it("finds nothing when the arms are the same", () => {
    const t = twoProportionTest({ exposures: 5000, conversions: 500 }, { exposures: 5000, conversions: 500 });
    expect(t.z).toBe(0);
    expect(t.pValue).toBeCloseTo(1, 6);
    expect(t.significant).toBe(false);
  });

  it("handles no conversions at all", () => {
    const t = twoProportionTest({ exposures: 10, conversions: 0 }, { exposures: 10, conversions: 0 });
    expect(t.z).toBe(0);
    expect(Number.isNaN(t.relativeLift)).toBe(true);
  });

  it("refuses broken counts", () => {
    expect(() => twoProportionTest({ exposures: 10, conversions: 11 }, { exposures: 10, conversions: 1 })).toThrow(
      /more than 10 exposures/
    );
    expect(() => twoProportionTest({ exposures: 0, conversions: 0 }, { exposures: 10, conversions: 1 })).toThrow(
      /positive whole number/
    );
    expect(() => twoProportionTest({ exposures: 10, conversions: 1.5 }, { exposures: 10, conversions: 1 })).toThrow(
      /whole number/
    );
  });
});

describe("sampleRatioCheck", () => {
  it("passes an even split", () => {
    const c = sampleRatioCheck(5000, 5000);
    expect(c.chiSquare).toBe(0);
    expect(c.mismatch).toBe(false);
  });

  it("matches a worked example: 5,000 against 5,200", () => {
    // χ² = 2 × 100² / 5100 = 3.9216, p = 0.0477: lopsided, but not past the 0.001 line.
    const c = sampleRatioCheck(5000, 5200);
    expect(c.chiSquare).toBeCloseTo(3.9216, 4);
    expect(c.pValue).toBeCloseTo(0.0477, 4);
    expect(c.mismatch).toBe(false);
  });

  it("flags a broken split", () => {
    const c = sampleRatioCheck(5000, 5500);
    expect(c.pValue).toBeLessThan(0.001);
    expect(c.mismatch).toBe(true);
  });

  it("refuses no exposures", () => {
    expect(() => sampleRatioCheck(0, 0)).toThrow(/no exposures/);
  });
});

describe("formatReadout", () => {
  it("prints the rates, lift, interval, p-value and the split", () => {
    const out = formatReadout({
      control: { exposures: 4000, conversions: 400 },
      treatment: { exposures: 4000, conversions: 480 },
    });
    expect(out).toContain("control:   400 / 4000 = 10.00%");
    expect(out).toContain("treatment: 480 / 4000 = 12.00%");
    expect(out).toContain("lift: +2.00 pp (+20.0% relative), 95% interval [+0.63 pp, +3.37 pp]");
    expect(out).toContain("z = 2.859, p = 0.00426 (significant at α = 0.05)");
    expect(out).toContain("sample ratio: 50.00% in treatment");
    expect(out).not.toContain("MISMATCH");
  });

  it("voids a result with a sample-ratio mismatch", () => {
    const out = formatReadout({
      control: { exposures: 5000, conversions: 500 },
      treatment: { exposures: 5500, conversions: 500 },
    });
    expect(out).toContain("SAMPLE RATIO MISMATCH");
    expect(out).toContain("-0.91 pp");
  });
});

describe("scripts/experiment-readout.mjs", () => {
  const script = path.resolve(__dirname, "../../../scripts/experiment-readout.mjs");
  const run = (...args: string[]) =>
    spawnSync(process.execPath, ["--disable-warning=MODULE_TYPELESS_PACKAGE_JSON", script, ...args], {
      encoding: "utf8",
    });

  it("prints the readout for four totals", () => {
    const r = run("4000", "400", "4000", "480");
    expect(r.status).toBe(0);
    expect(r.stdout).toContain("p = 0.00426");
    expect(r.stderr).toBe("");
  });

  it("prints its usage for anything else", () => {
    const r = run("4000", "400", "x");
    expect(r.status).toBe(2);
    expect(r.stderr).toContain("usage: experiment-readout");
  });

  it("refuses impossible counts", () => {
    const r = run("10", "11", "10", "1");
    expect(r.status).toBe(1);
    expect(r.stderr).toContain("the counts are broken");
  });
});
