import { expect, test, type Browser, type Page } from "@playwright/test";
import { COOKIE_NAME, dayStart, EXPERIMENTS } from "../src/lib/experiments/registry";
import { EXPERIMENTS_NOW } from "./helpers";

// The live experiment's proxy (docs/design/580-ab-experiments.md, Testing): a new visitor gets an
// arm in the jab_exp cookie, treatment is rewritten to the page's static sibling route, a second
// visit keeps the arm, and both arms come from the ISR cache. The web server judges the experiment
// at noon UTC on its first day (JAB_EXPERIMENTS_NOW in playwright.config.ts).
//
// Which arm served a page shows in its canonical link: the treatment route points search engines
// back to the control page, and the control page has none. The pages needn't be regenerated from
// the API for that (and the API's rate limit is better spent on e2e/a11y.spec.ts).

const experiment = EXPERIMENTS[0];

/** The control page's concrete path; an experiment on a dynamic route needs one picked by hand. */
const PATH = experiment?.path ?? "/";

/** The arm whose page this is, from its canonical link. */
async function servedArm(page: Page): Promise<"control" | "treatment"> {
  const canonical = await page.locator('link[rel="canonical"]').count();
  return canonical > 0 ? "treatment" : "control";
}

/** A fresh visitor's first and second visit: the cookies set, the arm each served, and the cache header. */
async function visitTwice(browser: Browser) {
  const context = await browser.newContext();
  const page = await context.newPage();
  const first = await page.goto(PATH);
  const firstStatus = first?.status();
  const firstCookie = await first?.headerValue("set-cookie");
  const cookie = (await context.cookies()).find((c) => c.name === COOKIE_NAME);
  const firstArm = await servedArm(page);
  const second = await page.goto(PATH);
  const secondCookie = await second?.headerValue("set-cookie");
  const secondCache = await second?.headerValue("x-nextjs-cache");
  const secondArm = await servedArm(page);
  const cookies = (await context.cookies()).filter((c) => c.name === COOKIE_NAME);
  await context.close();
  return { firstStatus, firstCookie, secondCookie, secondCache, cookie, cookies, firstArm, secondArm };
}

test.describe("the live experiment's proxy", () => {
  test.skip(!experiment, "no experiment in the registry");
  test.skip(PATH.includes("["), "the experiment's page is a dynamic route: pick a concrete path above");

  test("puts each new visitor in an arm, keeps it on the next visit, and serves both arms from the ISR cache", async ({
    browser,
  }) => {
    const seen = new Set<string>();
    // Each new visitor draws an arm at random: seeing only one in 20 visitors happens 1 time in 500,000.
    for (let i = 0; i < 20 && seen.size < 2; i++) {
      const visit = await visitTwice(browser);
      expect(visit.firstStatus).toBe(200);
      expect(visit.firstCookie, "the first visit sets the arm's cookie").toContain(`${COOKIE_NAME}=`);
      const arm = visit.cookie?.value.slice(experiment.id.length + 1);
      expect(visit.cookie?.value).toBe(`${experiment.id}.${arm}`);
      expect(visit.cookie).toMatchObject({ httpOnly: true, secure: true, sameSite: "Lax", path: "/" });
      // It lasts until the experiment ends, by the server's pinned clock (the browser adds Max-Age to its own).
      const maxAge = (dayStart(experiment.end) - Date.parse(EXPERIMENTS_NOW)) / 1000;
      expect(Math.abs((visit.cookie?.expires ?? 0) - Date.now() / 1000 - maxAge)).toBeLessThan(60);

      expect(visit.firstArm, "the page served is the cookie's arm").toBe(arm);
      expect(visit.secondArm, "a second visit keeps the arm").toBe(arm);
      expect(visit.cookies.map((c) => c.value)).toEqual([visit.cookie?.value]);
      expect(visit.secondCookie, "a second visit draws no new arm").toBeNull();
      // The rewrite keeps the page static: next start serves it from its ISR cache.
      expect(visit.secondCache).toMatch(/^(HIT|STALE)$/);
      seen.add(arm ?? "");
    }
    expect([...seen].sort()).toEqual(["control", "treatment"]);
  });

  test("gives crawlers and Global Privacy Control browsers the control page and no cookie", async ({ request }) => {
    const excluded: Record<string, string>[] = [
      { "sec-gpc": "1" },
      { "user-agent": "Mozilla/5.0 (compatible; Googlebot/2.1)" },
    ];
    for (const headers of excluded) {
      for (let i = 0; i < 5; i++) {
        const res = await request.get(PATH, { headers });
        expect(res.status()).toBe(200);
        expect(res.headers()["set-cookie"]).toBeUndefined();
        expect(await res.text()).not.toContain('rel="canonical"');
      }
    }
  });

  // The page's own links prefetch the plain path, which draws an arm, so only the forced responses
  // are checked for a cookie.
  test("shows either arm with ?variant= outside production, without a cookie", async ({ page }) => {
    for (const arm of ["treatment", "control", "treatment"] as const) {
      const res = await page.goto(`${PATH}?variant=${arm}`);
      expect(await servedArm(page)).toBe(arm);
      expect(await res?.headerValue("set-cookie")).toBeNull();
    }
  });
});
