import { expect, test, type BrowserContext, type Page, type Request } from "@playwright/test";
import { findReps, voteYeaOnHouseBill } from "./flows";
import {
  CA12_REP,
  COMPANION_TITLE,
  HOUSE_BILL,
  OTHER_MEMBERS,
  REPS_KEY,
  STUB_ADDRESS,
  STUB_LOCATION,
  VOTES_KEY,
} from "./helpers";

// The signed-out flow from #107 (docs/design/72-account-free-voting.md): find your reps, vote on a
// bill, compare on /scorecard. Votes and reps stay in the browser, so the only request that isn't
// a GET is the one rep lookup (POST /reps, the address in its body).

/** Every request the pages in a context make, with its method and body. */
function recordRequests(context: BrowserContext): Request[] {
  const requests: Request[] = [];
  context.on("request", (r) => requests.push(r));
  return requests;
}

/** Requests other than GET and HEAD, as "METHOD url" for a readable failure. */
function writes(requests: Request[]): string[] {
  return requests.filter((r) => !["GET", "HEAD"].includes(r.method())).map((r) => `${r.method()} ${r.url()}`);
}

async function storedJSON(page: Page, key: string): Promise<unknown> {
  return page.evaluate((k) => JSON.parse(window.localStorage.getItem(k) ?? "null"), key);
}

test("find reps: a signed-out visitor's address finds Ada Alvarez (House, CA-12), saved in the browser", async ({
  page,
  context,
}) => {
  const requests = recordRequests(context);

  await findReps(page);

  // CA-12's House member and no one else: the fixture has no California senators, and Ben Brooks
  // (TX-7) and Cora Chen (OH) sit elsewhere.
  // One card each (#667), with the party from GET /members/{id}.
  const cards = page.getByRole("main").getByRole("listitem");
  await expect(cards.getByRole("heading", { level: 3 })).toHaveText([CA12_REP.name]);
  await expect(cards.first()).toContainText(`House · ${CA12_REP.state}-${CA12_REP.district}`);
  await expect(cards.first()).toContainText("Democrat");

  // Saved: the state, district and members, never the address.
  const saved = await storedJSON(page, REPS_KEY);
  expect(saved).toMatchObject({
    state: CA12_REP.state,
    district: CA12_REP.district,
    members: [{ id: CA12_REP.bioguideId, name: CA12_REP.name, chamber: "House" }],
  });
  expect(JSON.stringify(saved)).not.toContain("E2E Way");

  // And still there after a reload.
  await page.reload();
  await expect(page.getByRole("heading", { level: 3, name: CA12_REP.name })).toBeVisible();

  // The lookup was the one write, and the address went in its body, not the URL.
  const lookups = requests.filter((r) => r.method() === "POST");
  expect(writes(requests)).toEqual([expect.stringMatching(/^POST http:\/\/localhost:\d+\/api\/v1\/reps$/)]);
  expect(lookups[0].postDataJSON()).toEqual({ address: STUB_ADDRESS });
  expect(requests.map((r) => r.url()).filter((u) => /E2E(%20|\+| )Way/i.test(u))).toEqual([]);
});

test("use my location: the browser's position finds Ada Alvarez, saved only once the visitor confirms", async ({
  page,
  context,
}) => {
  // The site's Permissions-Policy must allow geolocation for its own origin (#668), or this is denied.
  await context.grantPermissions(["geolocation"]);
  await context.setGeolocation(STUB_LOCATION);
  const requests = recordRequests(context);

  await page.goto("/scorecard");
  await page.getByRole("button", { name: "Use my location" }).click();

  const result = page.getByRole("status").filter({ hasText: CA12_REP.name });
  await expect(result).toContainText(`${CA12_REP.name} (House)`);
  await expect(result).toContainText(`within about ${STUB_LOCATION.accuracy} m`);
  expect(await storedJSON(page, REPS_KEY)).toBeNull();

  await result.getByRole("button", { name: "Use these representatives" }).click();
  await expect(page.getByRole("heading", { level: 2, name: /^Your representatives/ })).toBeVisible();
  expect(await storedJSON(page, REPS_KEY)).toMatchObject({
    state: CA12_REP.state,
    district: CA12_REP.district,
    members: [{ id: CA12_REP.bioguideId, chamber: "House" }],
  });

  // The point went once, in the body of POST /reps, and it isn't stored.
  expect(writes(requests)).toEqual([expect.stringMatching(/^POST http:\/\/localhost:\d+\/api\/v1\/reps$/)]);
  const lookup = requests.find((r) => r.method() === "POST");
  expect(lookup?.postDataJSON()).toEqual({ lat: STUB_LOCATION.latitude, lon: STUB_LOCATION.longitude });
  expect(JSON.stringify(await storedJSON(page, REPS_KEY))).not.toContain(String(STUB_LOCATION.latitude));
});

test("vote: a signed-out Yea on HR 1 survives a reload, is removed locally, and no request carries it", async ({ page, context }) => {
  const requests = recordRequests(context);

  await voteYeaOnHouseBill(page);

  await page.reload();
  await expect(page.getByText("Kept on this device only.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Yea", exact: true })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByRole("button", { name: "Nay", exact: true })).toHaveAttribute("aria-pressed", "false");

  const saved = await storedJSON(page, VOTES_KEY);
  expect(saved).toMatchObject({ v: 1, votes: { [HOUSE_BILL.id]: { vote: "yea", title: COMPANION_TITLE } } });

  // Removing it (#593) clears it from the browser, and stays local too.
  await page.getByRole("button", { name: "Remove my vote" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Your vote was removed." })).toBeVisible();
  await expect(page.getByRole("button", { name: "Yea", exact: true })).toHaveAttribute("aria-pressed", "false");
  expect(await storedJSON(page, VOTES_KEY)).toEqual({ v: 1, votes: {} });

  // Only reads left the browser: no POST, PUT or DELETE to the API or the web server, and no URL
  // with the vote in it.
  expect(writes(requests)).toEqual([]);
  expect(requests.map((r) => r.url()).filter((u) => /yea/i.test(u))).toEqual([]);
});

test("compare: Ada Alvarez's card shows 1 matching vote of 1 and links the row to HR 1", async ({
  page,
  context,
}) => {
  const requests = recordRequests(context);

  await findReps(page);
  await voteYeaOnHouseBill(page);
  await page.goto("/scorecard");

  // One card per saved member (#667): the stub address has only the CA-12 seat.
  const reps = page.getByRole("region", { name: /^Your representatives/ });
  const cards = reps.getByRole("list").first().locator(":scope > li");
  await expect(cards).toHaveCount(1);

  const card = cards.first();
  await expect(card.getByRole("heading", { level: 3 })).toHaveText(CA12_REP.name);
  await expect(card).toContainText("Voted with you");
  await expect(card).toContainText("1 of 1");
  await expect(card).toContainText("100%");
  await expect(card).toContainText("On 1 of the 1 bill you both voted yes or no on.");
  for (const name of OTHER_MEMBERS) await expect(page.getByRole("main")).not.toContainText(name);

  // The bills compared are listed on the card at desktop width, with both votes and the result.
  const row = card.getByRole("listitem").filter({ has: page.getByRole("link", { name: COMPANION_TITLE }) });
  await expect(row).toHaveCount(1);
  await expect(row.getByRole("link", { name: COMPANION_TITLE })).toHaveAttribute("href", `/bills/${HOUSE_BILL.id}`);
  await expect(row).toContainText("You Yea");
  await expect(row).toContainText("They Yea");
  await expect(row).toContainText("Match:");
  await expect(row).not.toContainText("No match");

  // Scoring happened here: the positions came from GETs, and the only write was the rep lookup.
  expect(writes(requests)).toEqual([expect.stringMatching(/\/api\/v1\/reps$/)]);
  expect(
    requests.some((r) => r.method() === "GET" && r.url().includes(`/members/${CA12_REP.bioguideId}/positions`)),
  ).toBe(true);
});

test("my votes: a signed-out vote is listed, changed, removed and restored on /my-votes, and no request carries it", async ({
  page,
  context,
}) => {
  const requests = recordRequests(context);

  await voteYeaOnHouseBill(page);
  await page.goto("/my-votes");
  await expect(page.getByRole("heading", { name: "1 vote" })).toBeVisible();
  await expect(page.getByText("Kept on this device only. Your votes aren't sent anywhere.")).toBeVisible();
  const row = page.getByRole("listitem").filter({ has: page.getByRole("link", { name: COMPANION_TITLE }) });
  await expect(row.getByRole("link", { name: COMPANION_TITLE })).toHaveAttribute("href", `/bills/${HOUSE_BILL.id}`);
  await expect(row).toContainText(`${HOUSE_BILL.label} · 119th Congress`);
  await expect(row).toContainText("Your vote: Yea");
  // Where it stands comes from the public list of bills past a chamber (#853), which H.R. 1 isn't on.
  await expect(row).toContainText("Not passed");
  await expect(page.getByRole("button", { name: "Became law 0" })).toBeEnabled();

  // Changing it here is changing it everywhere: the stored vote and the bill page.
  await row.getByRole("button", { name: `Change my vote on ${HOUSE_BILL.label}` }).click();
  const vote = row.getByRole("group", { name: `Your vote on ${HOUSE_BILL.label}` });
  await vote.getByRole("button", { name: "Nay", exact: true }).click();
  await expect(vote.getByRole("button", { name: "Nay", exact: true })).toHaveAttribute("aria-pressed", "true");
  expect(await storedJSON(page, VOTES_KEY)).toMatchObject({ v: 1, votes: { [HOUSE_BILL.id]: { vote: "nay" } } });

  await row.getByRole("button", { name: `Remove my vote on ${HOUSE_BILL.label}` }).click();
  await expect(row.getByRole("status")).toContainText(`Removed your vote on ${HOUSE_BILL.label}.`);
  expect(await storedJSON(page, VOTES_KEY)).toEqual({ v: 1, votes: {} });
  await row.getByRole("button", { name: `Undo removing my vote on ${HOUSE_BILL.label}` }).click();
  await expect(vote.getByRole("button", { name: "Nay", exact: true })).toHaveAttribute("aria-pressed", "true");

  await row.getByRole("link", { name: COMPANION_TITLE }).click();
  await expect(page.getByText("Kept on this device only.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Nay", exact: true })).toHaveAttribute("aria-pressed", "true");

  expect(writes(requests)).toEqual([]);
  expect(requests.map((r) => r.url()).filter((u) => /\b(yea|nay)\b/i.test(u))).toEqual([]);
  // The statuses are read for every congress, not only the one voted in, so they say nothing either.
  const statusLists = requests.map((r) => r.url()).filter((u) => u.includes("/api/v1/bill-statuses"));
  expect(statusLists.map((u) => new URL(u).searchParams.get("congress")).sort()).toEqual(
    expect.arrayContaining(["118", "119"]),
  );
});
