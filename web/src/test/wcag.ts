import type { Result } from "axe-core";

/**
 * The axe tags for WCAG 2.2 A and AA, the level the site meets. Vitest (src/test/axe.ts) and the
 * Playwright smoke tests (e2e/axe.ts) both run these.
 */
export const WCAG_TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"] as const;

/**
 * One line per element that fails a rule: the rule, what it asks for, the element's selector, why
 * it fails and the rule's help page, so a failure in a CI log says what to fix and where.
 */
export function formatViolations(violations: readonly Result[]): string[] {
  return violations.flatMap((v) =>
    v.nodes.map((n) => {
      const why = n.failureSummary?.replace(/\s+/g, " ").trim();
      return `${v.id}: ${v.help} at ${n.target.join(" ")}${why ? ` (${why})` : ""} ${v.helpUrl}`;
    }),
  );
}
