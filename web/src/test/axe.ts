import axe from "axe-core";
import { formatViolations, WCAG_TAGS } from "./wcag";

// The WCAG A and AA rules. color-contrast is off because jsdom doesn't lay out or paint, so
// axe can't compute colors here; src/lib/__tests__/contrast.test.ts checks the palette and the
// Playwright smoke tests (e2e/axe.ts) check the rendered pages.

/** Runs axe on a rendered subtree and returns one line per failing element (empty when clean). */
export async function axeViolations(root: Element): Promise<string[]> {
  const result = await axe.run(root, {
    runOnly: { type: "tag", values: [...WCAG_TAGS] },
    rules: { "color-contrast": { enabled: false } },
  });
  return formatViolations(result.violations);
}
