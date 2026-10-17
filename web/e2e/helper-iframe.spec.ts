import { expect, test, type Browser, type Page, type Route } from "@playwright/test";
import { helperIframeReady, signInThroughPopupFresh } from "./flows";

// How sign-in waits for the Auth emulator's helper iframe (#918), with no emulator: Playwright serves
// a page on the suite's own origin and a helper iframe on another localhost port, inserted as the
// Firebase SDK inserts it. Chromium doesn't render that iframe (offscreen, 1×1, cross-origin), so
// requestAnimationFrame stops in it after a frame or two: `frame.waitForFunction`'s default polling
// then evaluated its predicate once, and a helper that connected a few milliseconds later timed the
// popup sign-in out (about 1 in 20 accounts-on CI jobs).

/** The page under test, on the suite's own origin (helperIframeReady checks the helper's host). */
const PAGE_PATH = "/__e2e-helper-iframe";

/** The script the SDK and the helper iframe load from Google (gapi), which the stall case fails. */
const GOOGLE_SCRIPT = "https://apis.google.com/js/api.js";

/** How long the helper iframe takes to connect once its document has loaded. */
const CONNECT_AFTER_MS = 1_500;

/** The longest the wait may lag the connection: Node-side polls run every 250 ms at most. */
const MAX_LAG_MS = 750;

/** The page's origin, from the project's baseURL, and the helper's: the same host, the next port. */
function origins(): { page: string; helper: string } {
  const page = new URL(test.info().project.use.baseURL ?? "http://localhost:13000");
  const helper = new URL(page.origin);
  helper.port = String(Number(page.port || "80") + 1);
  return { page: page.origin, helper: helper.origin };
}

/**
 * The page: a Continue with Google button that opens a popup, and `insertHelper(src)`, which adds an
 * iframe with the SDK's attributes (`_openIframe`'s IFRAME_ATTRIBUTES, @firebase/auth 1.13.6).
 * `scripts` are extra script URLs the page loads.
 */
function pageHtml(scripts: string[] = []): string {
  return `<!doctype html><title>helper iframe</title>
${scripts.map((src) => `<script src="${src}"></script>`).join("\n")}
<button type="button" onclick="window.open('about:blank', 'popup', 'width=400,height=400')">Continue with Google</button>
<script>
  window.insertHelper = (src) => {
    const iframe = document.createElement("iframe");
    iframe.src = src;
    iframe.setAttribute("aria-hidden", "true");
    iframe.tabIndex = -1;
    iframe.style.cssText = "position:absolute;top:-100px;width:1px;height:1px";
    document.body.appendChild(iframe);
  };
  window.insertHelper(${JSON.stringify(`${origins().helper}/emulator/auth/iframe`)});
</script>`;
}

/**
 * The helper iframe's document, by its query: `connect` sets `parentContainer` (what firebase-tools'
 * /emulator/auth/iframe sets once it has connected to the page) CONNECT_AFTER_MS after load and
 * records when in `connectedAt`; `reload` first replaces the document with `?connect`, so the wait
 * spans a destroyed execution context; `alert` shows that text, as the emulator's iframe reports
 * its errors; anything else never connects.
 */
function helperHtml(query: URLSearchParams): string {
  return `<!doctype html><title>helper</title><script>
  const query = new URLSearchParams(${JSON.stringify(query.toString())});
  if (query.has("alert")) alert(query.get("alert"));
  window.addEventListener("load", () => {
    if (query.has("reload")) setTimeout(() => location.replace(location.pathname + "?connect"), 200);
    if (query.has("connect")) {
      setTimeout(() => {
        window.connectedAt = Date.now();
        window.parentContainer = {};
      }, ${CONNECT_AFTER_MS});
    }
  });
</script>`;
}

/** Serves the page at PAGE_URL's path and the helper iframe on the helper's origin. */
async function serve(page: Page, helperQuery: string, scripts: string[] = []) {
  const { page: pageOrigin, helper } = origins();
  await page.route(`${pageOrigin}${PAGE_PATH}`, (route) =>
    route.fulfill({ contentType: "text/html", body: pageHtml(scripts) }),
  );
  await page.route(`${helper}/emulator/auth/iframe**`, (route: Route) => {
    const url = new URL(route.request().url());
    // The page's first iframe gets this test's query; a reload or a second iframe brings its own.
    const query = url.search === "" ? new URLSearchParams(helperQuery) : url.searchParams;
    return route.fulfill({ contentType: "text/html", body: helperHtml(query) });
  });
}

/** The time a helper frame of `page` connected (its `connectedAt`), or 0 if none has. */
async function connectedAt(page: Page): Promise<number> {
  for (const frame of page.frames()) {
    const at = await frame.evaluate(() => (window as unknown as { connectedAt?: number }).connectedAt ?? 0);
    if (at > 0) return at;
  }
  return 0;
}

test.describe("helperIframeReady", () => {
  test("waits for an offscreen cross-origin helper that connects after it loads", async ({ page }) => {
    await serve(page, "connect");
    await page.goto(PAGE_PATH);
    await helperIframeReady(page);
    const returned = Date.now();
    const at = await connectedAt(page);
    expect(at, "the helper connected before the wait returned").toBeGreaterThan(0);
    expect(returned - at).toBeLessThan(MAX_LAG_MS);
  });

  test("keeps waiting when the helper's document is replaced mid-wait", async ({ page }) => {
    await serve(page, "reload");
    await page.goto(PAGE_PATH);
    await helperIframeReady(page);
    const helper = page.frames().find((f) => f.url().includes("/emulator/auth/iframe"))!;
    expect(new URL(helper.url()).search).toBe("?connect");
    expect(await connectedAt(page)).toBeGreaterThan(0);
  });

  test("passes once any helper iframe connects, when the first never does", async ({ page }) => {
    // The SDK opens a new helper iframe when it re-initializes (after auth/network-request-failed).
    await serve(page, "never");
    await page.goto(PAGE_PATH);
    await page.evaluate(
      (src) => (window as unknown as { insertHelper(s: string): void }).insertHelper(src),
      `${origins().helper}/emulator/auth/iframe?connect`,
    );
    await helperIframeReady(page);
    expect(await connectedAt(page)).toBeGreaterThan(0);
  });
});

// popupAttempt's verdict on a helper that never connects. Neither case involves the emulator, and each
// waits out a 10 s step, so they run once, in the accounts-off suite.
test.describe("a popup sign-in whose helper iframe never connects", () => {
  test.skip(!!process.env.E2E_AUTH_EMULATOR_HOST, "runs in the accounts-off suite only: no emulator involved");

  /** The error signInThroughPopupFresh fails with, given the helper's query and the page's scripts. */
  async function failure(browser: Browser, helperQuery: string, scripts: string[] = []): Promise<Error> {
    const result = await signInThroughPopupFresh(browser, PAGE_PATH, async () => {}, async (page) => {
      await serve(page, helperQuery, scripts);
      await page.route(GOOGLE_SCRIPT, (route) => route.abort("failed"));
    }, 1).then(
      () => new Error("signed in"),
      (err: unknown) => (err instanceof Error ? err : new Error(String(err))),
    );
    return result;
  }

  const warnings = () => test.info().annotations.filter((a) => a.type === "warning");

  test("fails at once, naming what the page alerted, when every Google request finished", async ({ browser }) => {
    const err = await failure(browser, "alert=Sending authEvent failed.");
    expect(err.message).toContain("though every apis.google.com request finished");
    expect(err.message).toContain('alert "Sending authEvent failed."');
    // Not taken for Google's script stalling: no warning, so no attempt in a fresh context.
    expect(warnings()).toEqual([]);
  });

  test("counts as a stall on Google's script when a request to apis.google.com failed", async ({ browser }) => {
    const err = await failure(browser, "never", [GOOGLE_SCRIPT]);
    expect(err.message).toMatch(/^Google's script didn't load in any of 1 popup sign-ins/);
    expect(err.message).toContain(`${GOOGLE_SCRIPT} failed (net::ERR_FAILED)`);
    expect(warnings().map((w) => w.description)).toEqual([
      expect.stringMatching(/^popup sign-in attempt 1 stalled on Google's script/),
    ]);
    // A warning marks a real stall in CI's logs: this one was on purpose.
    const annotations = test.info().annotations;
    annotations.splice(0, annotations.length, ...annotations.filter((a) => a.type !== "warning"));
  });
});
