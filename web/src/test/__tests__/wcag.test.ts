import type { Result } from "axe-core";
import { describe, expect, it } from "vitest";
import { formatViolations } from "../wcag";

const contrast = {
  id: "color-contrast",
  help: "Elements must meet minimum color contrast ratio thresholds",
  helpUrl: "https://dequeuniversity.com/rules/axe/4.11/color-contrast",
  nodes: [
    {
      target: ["main > p"],
      failureSummary: "Fix any of the following:\n  Element has insufficient color contrast of 3.1",
    },
    { target: ["iframe", "button"] },
  ],
} as unknown as Result;

describe("formatViolations", () => {
  it("gives each failing element its rule, selector, reason and help link", () => {
    expect(formatViolations([contrast])).toEqual([
      "color-contrast: Elements must meet minimum color contrast ratio thresholds at main > p " +
        "(Fix any of the following: Element has insufficient color contrast of 3.1) " +
        "https://dequeuniversity.com/rules/axe/4.11/color-contrast",
      "color-contrast: Elements must meet minimum color contrast ratio thresholds at iframe button " +
        "https://dequeuniversity.com/rules/axe/4.11/color-contrast",
    ]);
  });

  it("is empty when nothing fails", () => {
    expect(formatViolations([])).toEqual([]);
  });
});
