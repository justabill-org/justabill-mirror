import { expect, test } from "@playwright/test";
import { billCard, COMPANION_TITLE, HOUSE_BILL, SENATE_BILL } from "./helpers";

test("browse a bill: the list shows HR 1 and S 1 apart, and HR 1's page has the seeded data", async ({ page }) => {
  // Both are introduced, so they're in All, not in the default Laws view (#666).
  await page.goto("/bills?show=all");

  // HR 1 and S 1 share a number and a title; bill identity is congress + type + number, so they
  // are two cards linking to two pages.
  const cards = page.getByRole("main").getByRole("link", { name: new RegExp(COMPANION_TITLE) });
  await expect(cards).toHaveCount(2);

  // Visible cards in main only: the list streams in with a hidden copy of each card (#792).
  const houseCard = billCard(page, HOUSE_BILL.id);
  await expect(houseCard).toContainText(HOUSE_BILL.label);
  await expect(houseCard).toContainText(COMPANION_TITLE);

  const senateCard = billCard(page, SENATE_BILL.id);
  await expect(senateCard).toContainText(SENATE_BILL.label);
  await expect(senateCard).toContainText(COMPANION_TITLE);

  // The whole card still opens the bill, not only its title: here, a press in its top-left corner,
  // on its number, which the title's link covers.
  await houseCard.click({ position: { x: 12, y: 12 } });
  await expect(page).toHaveURL(`/bills/${HOUSE_BILL.id}`);

  const header = page.getByRole("main").locator("header");
  await expect(header.getByRole("heading", { level: 1 })).toHaveText(COMPANION_TITLE);
  await expect(header.locator(`a[href="/members/${HOUSE_BILL.sponsor.bioguideId}"]`)).toContainText(
    HOUSE_BILL.sponsor.name,
  );
  await expect(page.getByRole("tab", { name: `Votes (${HOUSE_BILL.rollCalls})` })).toBeVisible();
});

test("a link to a bill's #votes opens its Votes tab (#843, from My votes)", async ({ page }) => {
  await page.goto(`/bills/${HOUSE_BILL.id}#votes`);
  await expect(page.getByRole("tab", { name: `Votes (${HOUSE_BILL.rollCalls})` })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("tabpanel")).toBeInViewport();
});

test("share a bill: its page previews as itself, and Share links to that page (#810)", async ({ page }) => {
  // What a site's preview crawler reads: the tags in the page's <head> (Next.js streams metadata
  // into the body for browsers, but not for bots like this one).
  const res = await page.request.get(`/bills/${HOUSE_BILL.id}`, {
    headers: { "user-agent": "facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)" },
  });
  const head = (await res.text()).split("</head>")[0];
  const tag = (key: string) =>
    new RegExp(`<meta (?:property|name)="${key}" content="([^"]*)"`).exec(head)?.[1] ?? null;
  // Absolute on a deployment (NEXT_PUBLIC_SITE_URL); the e2e build has no site URL, so it's relative.
  const ogUrl = tag("og:url");
  expect(ogUrl, "og:url").not.toBeNull();
  const pageUrl = new URL(ogUrl!, res.url());
  expect(pageUrl.pathname).toBe(`/bills/${HOUSE_BILL.id}`);
  expect(tag("og:title")?.startsWith(`${HOUSE_BILL.label}: `)).toBe(true);
  expect(tag("og:description")).toMatch(/.+/);
  expect(tag("og:image")).toContain(`/bills/${HOUSE_BILL.id}/opengraph-image`);
  expect(tag("twitter:image")).toContain(`/bills/${HOUSE_BILL.id}/twitter-image`);

  await page.goto(`/bills/${HOUSE_BILL.id}?tab=votes#top`);
  const share = page.getByRole("button", { name: `Share ${HOUSE_BILL.label}` });
  await share.click();
  const sheet = page.getByRole("dialog", { name: "Share this bill" });
  await expect(sheet).toContainText(`${HOUSE_BILL.label}: ${COMPANION_TITLE}`);
  // The page's own query and hash never reach the shared link.
  const link = await sheet.getByLabel("Link").inputValue();
  expect(new URL(link).pathname + new URL(link).search + new URL(link).hash).toBe(`/bills/${HOUSE_BILL.id}`);
  expect(new URL(link).origin).toBe(pageUrl.origin);
  const x = new URL((await sheet.getByRole("link", { name: "X" }).getAttribute("href"))!);
  expect(x.searchParams.get("url")).toBe(link);
  await expect(sheet.getByRole("link", { name: "X" })).toHaveAttribute("target", "_blank");

  await page.keyboard.press("Escape");
  await expect(sheet).toBeHidden();
  await expect(share).toBeFocused();
});

test("share a bill from the list: the card's Share opens the sheet and stays on the list (#811)", async ({ page }) => {
  await page.goto("/bills?show=all&sort=introduced_date");
  const card = page
    .getByRole("main")
    .getByRole("listitem")
    .filter({ has: page.locator(`a[href="/bills/${HOUSE_BILL.id}"]`) });
  const share = card.getByRole("button", { name: `Share ${HOUSE_BILL.label}` });
  // WCAG 2.2's minimum target size.
  const box = await share.boundingBox();
  expect(box!.width).toBeGreaterThanOrEqual(24);
  expect(box!.height).toBeGreaterThanOrEqual(24);

  await share.click();
  const sheet = page.getByRole("dialog", { name: "Share this bill" });
  await expect(sheet).toContainText(`${HOUSE_BILL.label}: ${COMPANION_TITLE}`);
  const link = new URL(await sheet.getByLabel("Link").inputValue());
  expect(link.pathname + link.search + link.hash).toBe(`/bills/${HOUSE_BILL.id}`);
  await expect(page).toHaveURL(/\/bills\?show=all&sort=introduced_date$/);

  await page.keyboard.press("Escape");
  await expect(sheet).toBeHidden();
  await expect(share).toBeFocused();
  // By keyboard too: Enter on the focused button opens the sheet, not the bill.
  await page.keyboard.press("Enter");
  await expect(sheet).toBeVisible();
  await expect(page).toHaveURL(/\/bills\?show=all&sort=introduced_date$/);
});
