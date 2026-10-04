import { expect, test, type Browser, type BrowserContext, type Locator, type Page, type Request } from "@playwright/test";
// The note that someone signed in on this device (#757): without it the auth store never loads Firebase.
import { SIGNED_IN_KEY } from "../src/lib/auth/session-hint";
import { CROSS_SITE_STALL_ISSUE } from "./auth-emulator";
import { HOUSE_BILL, STUB_ADDRESS_FIELDS } from "./helpers";

// Steps more than one spec takes: the signed-out visitor's rep lookup and vote, and signing in.

/** Fills the stub address into a "Home address" form's fields (/scorecard and Settings share them). */
export async function fillAddress(form: Locator) {
  await form.getByLabel("Street address").fill(STUB_ADDRESS_FIELDS.street);
  await form.getByLabel("City").fill(STUB_ADDRESS_FIELDS.city);
  await form.getByLabel("State").selectOption(STUB_ADDRESS_FIELDS.state);
  await form.getByLabel("ZIP code").fill(STUB_ADDRESS_FIELDS.zip);
}

/** Finds the reps for the stub address on /scorecard, where a signed-out visitor looks them up. */
export async function findReps(page: Page) {
  await page.goto("/scorecard");
  // The form renders only after hydration (the server render is a skeleton), so it's live here.
  const form = page.getByRole("form", { name: "Home address" });
  await fillAddress(form);
  await form.getByRole("button", { name: "Find my representatives" }).click();
  // The rep cards replace the form once the lookup is stored.
  await expect(page.getByRole("heading", { level: 2, name: /^Your representatives/ })).toBeVisible();
}

/** Votes Yea on HR 1 from its bill page. */
export async function voteYeaOnHouseBill(page: Page) {
  await page.goto(`/bills/${HOUSE_BILL.id}`);
  // The note appears once the page has hydrated and read this browser's store, so the click lands.
  await expect(page.getByText("Kept on this device only.")).toBeVisible();
  const yea = page.getByRole("button", { name: "Yea", exact: true });
  await yea.click();
  await expect(yea).toHaveAttribute("aria-pressed", "true");
}

// Signing in (#250): the web's real sign-in page and Firebase SDK against the Auth emulator, as a
// subject the test picks, so the API can be asked about the same user.

/** The API the web build calls (playwright.config.ts starts it on this port). */
export const API_URL = `http://localhost:${process.env.E2E_API_PORT || "18080"}`;

/**
 * The emulator's host:port, as the API and these Node-side helpers see it. The browser may reach it
 * at another host (playwright.config.ts forwards localhost to it in CI, #794).
 */
function authEmulatorHost(): string {
  const host = process.env.E2E_AUTH_EMULATOR_HOST;
  if (!host) throw new Error("E2E_AUTH_EMULATOR_HOST is not set: run task e2e:sign-in");
  return host;
}

/** A subject no other test or earlier run has used: the emulator keeps its users between runs. */
export function uniqueSubject(): string {
  return `e2e-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}

/** The fake Google claims dev/auth-emulator/mint-token.sh sends for a subject, URL-encoded. */
function googleClaims(subject: string): string {
  return encodeURIComponent(JSON.stringify({ sub: subject, email: `${subject}@example.com`, email_verified: true }));
}

/** How long each step of an emulator popup sign-in may take before the attempt counts as stuck. */
const POPUP_STEP_MS = 10_000;
/** Popup sign-in attempts, each in a new browser context, before signInThroughPopupFresh gives up. */
const POPUP_ATTEMPTS = 3;
/** Test timeout for specs that sign in: room for a seeded sign-in and onboarding. */
export const SIGN_IN_TEST_TIMEOUT = 90_000;
/** Test timeout for the popup test: every popup attempt can wait out its helper iframe. */
export const POPUP_TEST_TIMEOUT = 150_000;

/**
 * The web build's Firebase API key, which names the SDK's stored session. `task e2e:sign-in` and CI
 * build with `fake-api-key`, and CI doesn't pass the variable on to the test container.
 */
const FIREBASE_API_KEY = process.env.NEXT_PUBLIC_FIREBASE_API_KEY || "fake-api-key";

/** What the emulator's accounts:signInWithIdp answers (the fields the SDK's session needs). */
type IdpSession = {
  localId: string;
  idToken: string;
  refreshToken: string;
  expiresIn: string;
  email?: string;
  federatedId?: string;
};

/** Signs `subject` in with the emulator's REST API, the way dev/auth-emulator/mint-token.sh does. */
async function emulatorSignIn(subject: string): Promise<IdpSession> {
  const url = `http://${authEmulatorHost()}/identitytoolkit.googleapis.com/v1/accounts:signInWithIdp?key=${FIREBASE_API_KEY}`;
  const res = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      postBody: `providerId=google.com&id_token=${googleClaims(subject)}`,
      requestUri: "http://localhost",
      returnSecureToken: true,
    }),
  });
  if (!res.ok) throw new Error(`the Auth emulator answered ${res.status}: ${await res.text()}`);
  const session = (await res.json()) as Partial<IdpSession>;
  if (!session.idToken || !session.localId || !session.refreshToken) {
    throw new Error("no idToken, localId or refreshToken in the Auth emulator's answer");
  }
  return session as IdpSession;
}

/**
 * The signed-in user as the Firebase JS SDK persists it (`UserImpl.toJSON`), under the key it reads
 * on start (`firebase:authUser:<apiKey>:[DEFAULT]`). The SDK checks localStorage as well as its
 * IndexedDB store for it, moves it to IndexedDB, and reloads the user from the emulator
 * (accounts:lookup) before the page sees it, so a session the emulator didn't issue never signs in.
 */
function storedUser(subject: string, session: IdpSession, now: number) {
  const email = session.email ?? `${subject}@example.com`;
  return {
    key: `firebase:authUser:${FIREBASE_API_KEY}:[DEFAULT]`,
    value: JSON.stringify({
      uid: session.localId,
      email,
      emailVerified: true,
      isAnonymous: false,
      providerData: [
        { providerId: "google.com", uid: subject, email, displayName: null, phoneNumber: null, photoURL: null },
      ],
      stsTokenManager: {
        refreshToken: session.refreshToken,
        accessToken: session.idToken,
        expirationTime: now + Number(session.expiresIn) * 1_000,
      },
      createdAt: String(now),
      lastLoginAt: String(now),
      apiKey: FIREBASE_API_KEY,
      appName: "[DEFAULT]",
    }),
  };
}

/**
 * Gives `page`'s browser context a Firebase session for `subject`, minted by the emulator, without
 * the popup: the session is written to the site's localStorage from a blank page on the site's
 * origin (served by Playwright, so no app code runs there), and the SDK picks it up on the next page
 * load (with the app's signed-in note set, as a real sign-in sets it). The API still gets, and
 * verifies, a real emulator ID token. The next visit to /login (or any page) is signed in; nothing
 * is sent to the API until then.
 */
export async function seedSession(page: Page, subject: string) {
  const { key, value } = storedUser(subject, await emulatorSignIn(subject), Date.now());
  const blank = new URL("/__e2e-seed-session", test.info().project.use.baseURL).toString();
  await page.route(blank, (route) => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>seed</title>" }));
  try {
    await page.goto(blank);
    await page.evaluate(
      ([k, v, hint]) => {
        localStorage.setItem(k, v);
        localStorage.setItem(hint, "1");
      },
      [key, value, SIGNED_IN_KEY],
    );
  } finally {
    await page.unroute(blank);
  }
}

/**
 * Signs in as `subject` without the popup (seedSession) and lands on `next`, through /login as a
 * real sign-in does: the page reads or creates the account (GET, then POST /me), and a new account,
 * or an existing one on a new browser, passes through onboarding, which is skipped.
 */
export async function signInSeeded(page: Page, subject: string, next = "/") {
  await seedSession(page, subject);
  await page.goto(`/login?next=${encodeURIComponent(next)}`);
  await page.waitForURL((url) => url.pathname !== "/login");
  if (new URL(page.url()).pathname === "/signup") {
    await page.getByRole("button", { name: "Continue" }).click();
  }
  await page.waitForURL((url) => url.pathname === next);
}

/**
 * Waits until the emulator's helper iframe in `page` can pass the popup's result on to the SDK.
 * The popup posts its result to that iframe, which only forwards it once it has loaded gapi and
 * connected to the page (`parentContainer`, in firebase-tools' /emulator/auth/iframe). A result that
 * comes earlier is parked in the iframe's sessionStorage and sent on connect, but on a slow machine
 * (CI's runners, or `docker run --cpus=1` locally) it then never reached the SDK and the popup stayed
 * open (PR #507). Waiting for the connection first avoids that race.
 */
async function helperIframeReady(page: Page) {
  await expect.poll(() => page.frames().some((f) => isHelperIframe(f.url())), { timeout: POPUP_STEP_MS }).toBe(true);
  const helper = page.frames().find((f) => isHelperIframe(f.url()))!;
  // playwright.config.ts checks the build's emulator host; this checks what the browser really loaded.
  const helperHost = new URL(helper.url()).hostname;
  const pageHost = new URL(page.url()).hostname;
  if (helperHost !== pageHost) {
    throw new CrossSiteEmulator(
      `the helper iframe is on ${helperHost}, another site than the page (${pageHost}): ` +
        `popup sign-ins stall that way (${CROSS_SITE_STALL_ISSUE}); build with the emulator on ${pageHost}`,
    );
  }
  await helper.waitForFunction(() => (window as unknown as { parentContainer: unknown }).parentContainer != null, null, {
    timeout: POPUP_STEP_MS,
  });
}

/**
 * Whether a URL is the emulator's helper iframe, which passes the popup's result on to the SDK. A
 * frame whose document hasn't arrived yet has an empty URL, which isn't.
 */
export function isHelperIframe(url: string): boolean {
  return URL.canParse(url) && new URL(url).pathname === "/emulator/auth/iframe";
}

/** The browser reaches the emulator on another site than the page: a setup error, never retried. */
class CrossSiteEmulator extends Error {}

/** Whether a URL is one of Google's scripts the emulator's helper iframe loads (gapi). */
function isGoogleApiScript(url: string): boolean {
  return new URL(url).hostname === "apis.google.com";
}

/**
 * Watches `context` for requests to apis.google.com, and says whether any of them hasn't finished
 * well: still pending, failed or aborted, or answered with an error status. That tells a sign-in
 * stuck on Google's script (#794) from one stuck on our code: with every such request answered, a
 * helper iframe that never connects is our failure, not Google's.
 */
function watchGoogleApiRequests(context: BrowserContext): () => string[] {
  const unfinished = new Map<Request, string>();
  context.on("request", (r) => {
    if (isGoogleApiScript(r.url())) unfinished.set(r, `${r.url()} pending`);
  });
  context.on("requestfailed", (r) => {
    if (unfinished.has(r)) unfinished.set(r, `${r.url()} failed (${r.failure()?.errorText ?? "unknown"})`);
  });
  context.on("requestfinished", async (r) => {
    if (!unfinished.has(r)) return;
    const status = (await r.response())?.status() ?? 0;
    if (status >= 200 && status < 400) unfinished.delete(r);
    else unfinished.set(r, `${r.url()} answered ${status}`);
  });
  return () => [...unfinished.values()];
}

/** A popup attempt that stopped because Google's script didn't load (#794), not because of our code. */
class HelperIframeStall extends Error {}

/**
 * One popup sign-in on `page`, already on /login: clicks Continue with Google, lets `complete`
 * finish the emulator's popup, and waits for the popup to close, which means the SDK got the result.
 * The SDK loads gapi from apis.google.com before it opens the popup, and the helper iframe loads it
 * again before it connects. If the popup never opens or the iframe never connects while a request to
 * apis.google.com didn't finish well, it throws HelperIframeStall; anything else that goes wrong (the
 * button never enables, either step failing with every Google request answered, the popup not
 * closing once completed) throws a plain error.
 */
async function popupAttempt(
  page: Page,
  complete: (popup: Page) => Promise<void>,
  googleTrouble: () => string[],
): Promise<void> {
  // Enabled once the SDK has loaded and found no session.
  const google = page.getByRole("button", { name: "Continue with Google" });
  await expect(google).toBeEnabled();
  let popup: Page;
  try {
    const popupOpened = page.waitForEvent("popup", { timeout: POPUP_STEP_MS });
    await google.click();
    popup = await popupOpened;
    await helperIframeReady(page);
  } catch (err) {
    if (err instanceof CrossSiteEmulator) throw err;
    const trouble = googleTrouble();
    const frames = page.frames().map((f) => f.url()).join(", ");
    const why = `${err instanceof Error ? err.message.split("\n")[0] : String(err)} (frames: ${frames})`;
    if (trouble.length > 0) throw new HelperIframeStall(`${why}; apis.google.com: ${trouble.join("; ")}`);
    throw new Error(`${why}, though every apis.google.com request finished`, { cause: err });
  }
  await complete(popup);
  await expect.poll(() => popup.isClosed(), { message: "the popup closes", timeout: POPUP_STEP_MS }).toBe(true);
}

/**
 * Signs in through the emulator's Google popup in a new context of `browser`: opens a page,
 * lets `prepare` attach its listeners and routes, visits `start` (a /login URL), and runs the popup
 * with `complete`. Returns the signed-in page; the caller closes its context.
 *
 * The helper iframe's request for apis.google.com/js/api.js used to stall in CI (#777: about 1
 * sign-in in 60). #794 found why: the popup opened before the iframe asked for the script while the
 * emulator was on another site than the page, and Chromium (or Playwright) then never sent it. The
 * browser now reaches the emulator on localhost, as the page (playwright.config.ts), so that order
 * is harmless (sign-in.spec.ts forces it). Should Google's script still not load, a stuck attempt's
 * context is closed and the next one starts in a new context (attempts in one context stall
 * together), up to `attempts` times, each with a `warning` annotation, and then the test fails.
 * Any other failure (see popupAttempt) fails the test at once.
 */
export async function signInThroughPopupFresh(
  browser: Browser,
  start: string,
  complete: (popup: Page) => Promise<void>,
  prepare: (page: Page) => void | Promise<void> = () => {},
  attempts = POPUP_ATTEMPTS,
): Promise<Page> {
  for (let attempt = 1; ; attempt++) {
    // The project's options (Desktop Chrome's viewport and user agent, baseURL), as the page fixture gets.
    const context = await browser.newContext(test.info().project.use);
    const googleTrouble = watchGoogleApiRequests(context);
    try {
      const page = await context.newPage();
      await prepare(page);
      await page.goto(start);
      await popupAttempt(page, complete, googleTrouble);
      return page;
    } catch (err) {
      await context.close();
      if (!(err instanceof HelperIframeStall)) throw err;
      const why = `popup sign-in attempt ${attempt} stalled on Google's script: ${err.message}`;
      test.info().annotations.push({ type: "warning", description: why });
      if (attempt >= attempts) {
        throw new Error(`Google's script didn't load in any of ${attempts} popup sign-ins (last: ${err.message})`, {
          cause: err,
        });
      }
    }
  }
}

/** An ID token for `subject` straight from the emulator, the way mint-token.sh gets one. */
export async function idToken(subject: string): Promise<string> {
  return (await emulatorSignIn(subject)).idToken;
}
