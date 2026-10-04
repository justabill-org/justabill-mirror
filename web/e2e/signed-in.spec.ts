import { expect, test, type APIRequestContext, type APIResponse } from "@playwright/test";
import { API_URL, idToken, SIGN_IN_TEST_TIMEOUT, signInSeeded, uniqueSubject } from "./flows";
import { HOUSE_BILL } from "./helpers";

// The signed-in smoke test (#250, docs/design/84-e2e-smoke-tests.md "Recommendation: Auth"): a
// session the Firebase Auth emulator minted (signInSeeded: no popup, #777), a vote into the account,
// and the API's own record of it. The API verifies the emulator's ID tokens like any others: it has
// no dev-auth bypass. Runs under `task e2e:sign-in` and in CI's Web E2E job; skipped without the
// emulator.

test.skip(!process.env.E2E_AUTH_EMULATOR_HOST, "needs the Auth emulator: task e2e:sign-in");
test.describe.configure({ timeout: SIGN_IN_TEST_TIMEOUT });

/**
 * GET /me/votes, waiting out the API's rate limit on /me routes: the sign-in specs before this one
 * use up its window, and a 429 would say nothing about auth.
 */
async function getMyVotes(request: APIRequestContext, headers: Record<string, string>): Promise<APIResponse> {
  for (;;) {
    const res = await request.get(`${API_URL}/api/v1/me/votes`, { headers });
    if (res.status() !== 429) return res;
    await new Promise((r) => setTimeout(r, Number(res.headers()["retry-after"] ?? "60") * 1_000));
  }
}

type MyVotes = { items: { bill_id: string; vote: string }[] };

test("a signed-in Yea on HR 1 is in the account: GET /me/votes lists it, and a second browser shows it", async ({
  page,
  browser,
  request,
}) => {
  const subject = uniqueSubject();
  await signInSeeded(page, subject, `/bills/${HOUSE_BILL.id}`);

  // The buttons wait for the account's votes to load; the click then goes to the API with the token.
  const cast = page.waitForRequest((r) => r.method() === "POST" && r.url() === `${API_URL}/api/v1/bills/${HOUSE_BILL.id}/vote`);
  const saved = page.waitForResponse((r) => r.request().method() === "POST" && r.url().endsWith(`/bills/${HOUSE_BILL.id}/vote`));
  const yea = page.getByRole("button", { name: "Yea", exact: true });
  await yea.click();
  expect((await cast).headers()["authorization"]).toMatch(/^Bearer \S+\.\S*\./);
  expect((await saved).status()).toBe(200);
  await expect(yea).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByText(/Kept on this device/)).toHaveCount(0);

  // The API, not the page (#82): the same subject's token, minted the way mint-token.sh does.
  const res = await getMyVotes(request, { Authorization: `Bearer ${await idToken(subject)}` });
  expect(res.status()).toBe(200);
  const mine = (await res.json()) as MyVotes;
  expect(mine.items.map((v) => [v.bill_id, v.vote])).toEqual([[HOUSE_BILL.id, "yea"]]);

  // Another browser, the same person: the vote comes from the account, not this device's storage.
  const other = await browser.newContext();
  try {
    const page2 = await other.newPage();
    await signInSeeded(page2, subject, `/bills/${HOUSE_BILL.id}`);
    await expect(page2.getByRole("button", { name: "Yea", exact: true })).toHaveAttribute("aria-pressed", "true");
  } finally {
    await other.close();
  }
});

test("removing a signed-in vote deletes it from the account: GET /me/votes no longer lists it", async ({
  page,
  request,
}) => {
  const subject = uniqueSubject();
  await signInSeeded(page, subject, `/bills/${HOUSE_BILL.id}`);
  const yea = page.getByRole("button", { name: "Yea", exact: true });
  const saved = page.waitForResponse((r) => r.request().method() === "POST" && r.url().endsWith(`/bills/${HOUSE_BILL.id}/vote`));
  await yea.click();
  expect((await saved).status()).toBe(200);
  await expect(yea).toHaveAttribute("aria-pressed", "true");

  // #593: the click sends DELETE /bills/{id}/vote with the token, and the vote leaves the page.
  const removed = page.waitForResponse(
    (r) => r.request().method() === "DELETE" && r.url() === `${API_URL}/api/v1/bills/${HOUSE_BILL.id}/vote`
  );
  await page.getByRole("button", { name: "Remove my vote" }).click();
  const res = await removed;
  expect(res.status()).toBe(204);
  expect(res.request().headers()["authorization"]).toMatch(/^Bearer \S+\.\S*\./);
  await expect(page.getByRole("status").filter({ hasText: "Your vote was removed." })).toBeVisible();
  await expect(yea).toHaveAttribute("aria-pressed", "false");

  const mine = await getMyVotes(request, { Authorization: `Bearer ${await idToken(subject)}` });
  expect(mine.status()).toBe(200);
  expect(((await mine.json()) as MyVotes).items).toEqual([]);
});

test("on /my-votes, a signed-in change is a POST and a removal a DELETE, and the account records both", async ({
  page,
  request,
}) => {
  const subject = uniqueSubject();
  await signInSeeded(page, subject, `/bills/${HOUSE_BILL.id}`);
  const yea = page.getByRole("button", { name: "Yea", exact: true });
  const saved = page.waitForResponse((r) => r.request().method() === "POST" && r.url().endsWith(`/bills/${HOUSE_BILL.id}/vote`));
  await yea.click();
  expect((await saved).status()).toBe(200);

  await page.goto("/my-votes");
  await expect(page.getByRole("heading", { name: "1 vote" })).toBeVisible();
  await expect(page.getByText(/^Saved in your account\./)).toBeVisible();
  const row = page.getByRole("listitem").filter({ has: page.getByRole("link", { name: /Companion Act/ }) });
  await row.getByRole("button", { name: `Change my vote on ${HOUSE_BILL.label}` }).click();
  const vote = row.getByRole("group", { name: `Your vote on ${HOUSE_BILL.label}` });

  const changed = page.waitForResponse(
    (r) => r.request().method() === "POST" && r.url() === `${API_URL}/api/v1/bills/${HOUSE_BILL.id}/vote`
  );
  await vote.getByRole("button", { name: "Nay", exact: true }).click();
  const change = await changed;
  expect(change.status()).toBe(200);
  expect(change.request().postDataJSON()).toMatchObject({ vote: "nay" });
  await expect(vote.getByRole("button", { name: "Nay", exact: true })).toHaveAttribute("aria-pressed", "true");
  const afterChange = await getMyVotes(request, { Authorization: `Bearer ${await idToken(subject)}` });
  expect(((await afterChange.json()) as MyVotes).items.map((v) => [v.bill_id, v.vote])).toEqual([[HOUSE_BILL.id, "nay"]]);

  const removed = page.waitForResponse(
    (r) => r.request().method() === "DELETE" && r.url() === `${API_URL}/api/v1/bills/${HOUSE_BILL.id}/vote`
  );
  await row.getByRole("button", { name: `Remove my vote on ${HOUSE_BILL.label}` }).click();
  expect((await removed).status()).toBe(204);
  await expect(row.getByRole("status")).toContainText(`Removed your vote on ${HOUSE_BILL.label}.`);
  const afterRemove = await getMyVotes(request, { Authorization: `Bearer ${await idToken(subject)}` });
  expect(((await afterRemove.json()) as MyVotes).items).toEqual([]);
});

test("the API takes no dev-auth shortcut: /me/votes needs a real token", async ({ request }) => {
  // Neither the pre-#173 dev header nor a made-up bearer gets in; only the emulator's tokens do.
  const attempts: Record<string, string>[] = [{}, { "X-Dev-User-Id": "e2e-dev" }, { Authorization: "Bearer not-a-token" }];
  for (const headers of attempts) {
    const res = await getMyVotes(request, headers);
    expect(res.status(), JSON.stringify(headers)).toBe(401);
  }
});
