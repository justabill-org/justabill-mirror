import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import * as names from "../obs/names";

// names.ts and obs/semconv/names.go are generated from one registry (obs/semconv/registry); CI's
// `scripts/semconv.sh --check` keeps both current. This checks the two templates agree.
const goNames = (): string[] => {
  const src = readFileSync(path.resolve(__dirname, "../../../../obs/semconv/names.go"), "utf8");
  return [...src.matchAll(/^const \w+(?:Key|Name|Event) = (?:attribute\.Key\()?"([^"]+)"/gm)].map((m) => m[1]);
};

const tsNames = (): string[] =>
  Object.entries(names)
    .filter(([key]) => /^(ATTR|METRIC|EVENT)_/.test(key) && !/_(UNIT|DESCRIPTION)$/.test(key))
    .map(([, value]) => value);

describe("obs names", () => {
  it("are all justabill.* names", () => {
    expect(tsNames().length).toBeGreaterThan(0);
    for (const name of tsNames()) {
      expect(name).toMatch(/^justabill\./);
    }
  });

  it("match the Go names", () => {
    expect(tsNames().sort()).toEqual(goNames().sort());
  });

  it("carry the registry's units and values", () => {
    expect(names.ATTR_JOB_NAME).toBe("justabill.job.name");
    expect(names.JOB_OUTCOME_VALUE_FAILED).toBe("failed");
    expect(names.METRIC_WEB_FIXTURE_FALLBACK).toBe("justabill.web.fixture_fallback");
    expect(names.METRIC_WEB_FIXTURE_FALLBACK_UNIT).toBe("{render}");
    expect(names.EVENT_PIPELINE_JOB_FINISHED).toBe("justabill.pipeline.job.finished");
  });
});
