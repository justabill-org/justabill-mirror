import AxeBuilder from "@axe-core/playwright";
import { expect, type Page } from "@playwright/test";
import { formatViolations, WCAG_TAGS } from "../src/test/wcag";

/**
 * Runs axe with the WCAG 2.2 A and AA rules on the page as it is now, color contrast included, and
 * fails the test with one line per failing element (rule, selector, reason, help link).
 *
 * Call it once the content under test is on screen: a skeleton or a sheet halfway through its
 * slide-in has other colors than the finished page.
 */
export async function expectAccessible(page: Page, what: string): Promise<void> {
  await settle(page);
  const { violations } = await new AxeBuilder({ page }).withTags([...WCAG_TAGS]).analyze();
  expect(formatViolations(violations), `axe on ${what}`).toEqual([]);
}

/** Waits for running CSS transitions and animations to end, so axe sees the final colors. */
async function settle(page: Page): Promise<void> {
  await page.waitForFunction(() =>
    document.getAnimations().every((a) => a.playState !== "running" || a.effect?.getTiming().iterations === Infinity),
  );
}
