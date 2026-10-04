import { expect, test } from "@playwright/test";
import { expectAccessible } from "./axe";
import { findReps, voteYeaOnHouseBill } from "./flows";
import {
  billCard,
  CA12_REP,
  CRA_BILL,
  CRA_UNMATCHED_BILL,
  HOUSE_BILL,
  LAW_BILL,
  SENATE_BILL,
  STUB_LOCATION,
} from "./helpers";
import { regenerate } from "./prerender";

// Accessibility (#83, #336): axe's WCAG 2.2 A and AA rules, color contrast included, on every page
// the smoke tests visit and on each sheet and panel those pages open, in both color schemes. The
// site follows the system setting (prefers-color-scheme in globals.css), which colorScheme sets.

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`${colorScheme} scheme`, () => {
    test.use({ colorScheme });

    test(`home page is accessible (${colorScheme})`, async ({ page }) => {
      await page.goto("/");
      await expect(page.getByRole("main")).toBeVisible();
      await expectAccessible(page, `/ (${colorScheme})`);
    });

    test(`bill list, its views and its filter panel are accessible (${colorScheme})`, async ({ page }) => {
      // Five axe runs: past the 30s default on a busy CI runner.
      test.slow();
      // Laws is the default view (#666).
      await page.goto("/bills");
      await expect(billCard(page, LAW_BILL.id)).toBeVisible();
      await expectAccessible(page, `/bills (${colorScheme})`);

      const filters = page.getByRole("button", { name: "Filters" });
      await filters.click();
      await expect(filters).toHaveAttribute("aria-expanded", "true");
      await expectAccessible(page, `/bills with the filter panel open (${colorScheme})`);

      await page.getByRole("navigation", { name: "Bill views" }).getByRole("link", { name: /^All/ }).click();
      await expect(page).toHaveURL(/show=all/);
      await expect(billCard(page, HOUSE_BILL.id)).toBeVisible();
      await expectAccessible(page, `/bills?show=all (${colorScheme})`);

      await page.goto("/bills?show=committee&sort=introduced_date");
      await expect(page.getByRole("heading", { level: 1, name: "Bills" })).toBeVisible();
      await expectAccessible(page, `/bills In committee by date introduced (${colorScheme})`);

      // A policy area no seeded bill has (#796): an empty list, and the area's select in the panel.
      await page.goto("/bills?area=Health");
      await expect(page.getByRole("heading", { name: "No bills found" })).toBeVisible();
      await page.getByRole("button", { name: /^Filters/ }).click();
      await expect(page.getByLabel("Policy area")).toHaveValue("Health");
      await expectAccessible(page, `/bills with a policy area (${colorScheme})`);
    });

    test(`bill list on a phone is accessible (${colorScheme})`, async ({ page }) => {
      await page.setViewportSize({ width: 390, height: 844 });
      await page.goto("/bills?show=all");
      await expect(billCard(page, HOUSE_BILL.id)).toBeVisible();
      // One column, and the views row scrolls by itself rather than widening the page.
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
      await expectAccessible(page, `/bills at 390px (${colorScheme})`);
    });

    // #688: the numbered page row made the page scroll sideways below 360px. One bill a page gives the
    // seeded list several pages; below sm the nav shows Previous, "Page N of M" and Next.
    test(`bill list pagination on a phone is accessible and fits (${colorScheme})`, async ({ page }) => {
      await page.setViewportSize({ width: 390, height: 844 });
      await page.goto("/bills?show=all&limit=1");
      const pagination = page.getByRole("navigation", { name: "Pagination" });
      const current = pagination.getByText(/^Page 1 of \d+$/);
      await expect(current).toBeVisible();
      await expect(current).toHaveAttribute("aria-current", "page");
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
      await expectAccessible(page, `/bills pagination at 390px (${colorScheme})`);

      await page.setViewportSize({ width: 320, height: 640 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);
      for (const name of ["Previous page", "Next page"]) {
        const box = await pagination.getByRole("button", { name }).boundingBox();
        expect(box?.width).toBeGreaterThanOrEqual(24);
        expect(box?.height).toBeGreaterThanOrEqual(24);
      }
      await pagination.getByRole("button", { name: "Next page" }).click();
      await expect(page).toHaveURL(/offset=1/);
      await expect(pagination.getByText(/^Page 2 of \d+$/)).toBeVisible();
    });

    test(`bill page, each of its tabs and the share sheet are accessible (${colorScheme})`, async ({ page }) => {
      // Six or more axe runs: past the 30s default on a busy CI runner.
      test.slow();
      await page.goto(`/bills/${HOUSE_BILL.id}`);
      await expect(page.getByText("Kept on this device only.")).toBeVisible();
      await expectAccessible(page, `/bills/${HOUSE_BILL.id} (${colorScheme})`);

      const tabs = page.getByRole("tab");
      const names = await tabs.allTextContents();
      expect(names.length).toBeGreaterThan(1);
      for (const name of names) {
        const tab = page.getByRole("tab", { name, exact: true });
        await tab.click();
        await expect(tab).toHaveAttribute("aria-selected", "true");
        await expectAccessible(page, `/bills/${HOUSE_BILL.id}, ${name} tab (${colorScheme})`);
      }

      // The bill's own Share sheet (#810), then closed again before voting.
      await page.getByRole("button", { name: `Share ${HOUSE_BILL.label}` }).click();
      await expect(page.getByRole("dialog", { name: "Share this bill" })).toBeVisible();
      await expectAccessible(page, `/bills/${HOUSE_BILL.id} bill share sheet (${colorScheme})`);
      await page.keyboard.press("Escape");
      await expect(page.getByRole("dialog")).toBeHidden();

      // A vote shows the share button; its sheet is a modal dialog over the page.
      await voteYeaOnHouseBill(page);
      await expectAccessible(page, `/bills/${HOUSE_BILL.id} after a Yea vote (${colorScheme})`);
      await page.getByRole("button", { name: "Share my vote" }).click();
      await expect(page.getByRole("dialog", { name: "Share this card" })).toBeVisible();
      await expectAccessible(page, `/bills/${HOUSE_BILL.id} share sheet (${colorScheme})`);
    });

    test(`a bill's Text tab with its companion bills open is accessible (${colorScheme})`, async ({ page }) => {
      await page.goto(`/bills/${HOUSE_BILL.id}`);
      await expect(page.getByText("Kept on this device only.")).toBeVisible();
      await page.getByRole("tab", { name: /^Text/ }).click();
      // Congress.gov lists S. 1 as HR 1's identical bill: the Senate companion.
      const companions = page.getByRole("button", { name: /^Companion bills/ });
      await expect(companions).toHaveAttribute("aria-expanded", "false");
      await companions.click();
      await expect(companions).toHaveAttribute("aria-expanded", "true");
      await expect(page.getByRole("link", { name: new RegExp(`Senate companion: ${SENATE_BILL.label}`) })).toHaveAttribute(
        "href",
        `/bills/${SENATE_BILL.id}`,
      );
      await expectAccessible(page, `/bills/${HOUSE_BILL.id} Text tab with companion bills open (${colorScheme})`);
    });

    test(`member page, on desktop and a phone, is accessible (${colorScheme})`, async ({ page }) => {
      await page.goto(`/members/${CA12_REP.bioguideId}`);
      await expect(page.getByRole("heading", { level: 1 })).toContainText(CA12_REP.name);
      // The fixture's vote on HR 1, linking to the bill, and the member's one term (#787).
      const votes = page.getByRole("region", { name: "Recent votes" });
      await expect(votes.locator(`a[href="/bills/${HOUSE_BILL.id}"]`)).toBeVisible();
      await expect(page.getByRole("region", { name: "Terms" }).getByRole("listitem")).toHaveCount(1);
      await expectAccessible(page, `/members/${CA12_REP.bioguideId} (${colorScheme})`);

      await page.setViewportSize({ width: 390, height: 844 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
      await expectAccessible(page, `/members/${CA12_REP.bioguideId} at 390px (${colorScheme})`);
    });

    test(`scorecard, before and after a lookup and a vote, on desktop and a phone, is accessible (${colorScheme})`, async ({
      page,
      context,
    }) => {
      // Five axe runs: past the 30s default on a busy CI runner.
      test.slow();
      await page.goto("/scorecard");
      await expect(page.getByRole("form", { name: "Home address" })).toBeVisible();
      await expectAccessible(page, `/scorecard with no reps saved (${colorScheme})`);

      // "Use my location" (#668): the result it shows before the visitor confirms it.
      await context.grantPermissions(["geolocation"]);
      await context.setGeolocation(STUB_LOCATION);
      await page.getByRole("button", { name: "Use my location" }).click();
      await expect(page.getByRole("button", { name: "Use these representatives" })).toBeVisible();
      await expectAccessible(page, `/scorecard with a location result (${colorScheme})`);
      await page.getByRole("button", { name: "Clear", exact: true }).click();

      // #667: the cards show before any votes, under a Start voting banner.
      await findReps(page);
      await expect(page.getByRole("link", { name: "Start voting" })).toBeVisible();
      await expectAccessible(page, `/scorecard with rep cards before any votes (${colorScheme})`);

      await voteYeaOnHouseBill(page);
      await page.goto("/scorecard");
      await expect(page.getByRole("heading", { level: 4, name: "Bills compared" })).toBeVisible();
      await expectAccessible(page, `/scorecard with a scored rep card (${colorScheme})`);

      // On a phone each card folds its bills away behind a disclosure.
      await page.setViewportSize({ width: 390, height: 844 });
      const toggle = page.getByRole("button", { name: "See the 1 bill compared" });
      await expect(toggle).toHaveAttribute("aria-expanded", "false");
      await expectAccessible(page, `/scorecard at 390px with the bills folded (${colorScheme})`);
      await toggle.click();
      await expect(page.getByRole("button", { name: "Show fewer" })).toHaveAttribute("aria-expanded", "true");
      await expectAccessible(page, `/scorecard at 390px with the bills open (${colorScheme})`);
    });

    test(`vote page, its filter, its card and the card's Details and Share sheets are accessible (${colorScheme})`, async ({
      page,
    }) => {
      // Four axe runs: past the 30s default on a busy CI runner.
      test.slow();
      // The build prerendered /vote without the API; render it from the fixture's laws.
      await regenerate(page, "/vote");
      await page.goto("/vote");
      await expect(page.getByRole("heading", { level: 2, name: LAW_BILL.title })).toBeVisible();
      // #662: the card's facts come from GET /bills?include=card (the fixture's law, enacted 2025-07-15).
      await expect(page.getByRole("definition").filter({ hasText: "Jul 15, 2025" })).toBeVisible();
      await expectAccessible(page, `/vote (${colorScheme})`);

      // #797: the filter opens as a panel with the /bills views and fields, Laws chosen.
      const filter = page.getByRole("button", { name: /^Filter:/ });
      await filter.click();
      await expect(filter).toHaveAttribute("aria-expanded", "true");
      await expect(page.getByRole("radio", { name: "Laws" })).toBeChecked();
      await expectAccessible(page, `/vote with its filter open (${colorScheme})`);
      // A view is a new URL whose deck the browser reads from the API; Back returns to the Laws deck.
      // The radio itself is visually hidden: click its segment, as a visitor does.
      await page.locator("label").filter({ has: page.getByRole("radio", { name: "All" }) }).click();
      await expect(page).toHaveURL(/\/vote\?show=all$/);
      await expect(page.getByText(/^All: [\d,]+ of [\d,]+ bills left$/)).toBeVisible();
      await expect(page.getByRole("heading", { level: 2 })).toBeVisible();
      await page.goBack();
      await expect(page).toHaveURL(/\/vote$/);
      await expect(page.getByRole("heading", { level: 2, name: LAW_BILL.title })).toBeVisible();
      if ((await filter.getAttribute("aria-expanded")) === "true") await page.getByRole("button", { name: "Done" }).click();

      await page.getByRole("button", { name: "Details" }).click();
      const details = page.getByRole("dialog", { name: LAW_BILL.title });
      await expect(details.getByRole("heading", { name: "AI summary", exact: true })).toBeVisible();
      await expect(details.getByRole("heading", { name: "Where it stands" })).toBeVisible();
      await expectAccessible(page, `/vote Details sheet (${colorScheme})`);
      await page.keyboard.press("Escape");

      // #811: the card's Share sheet, which closes back onto the same card.
      const share = page.getByRole("button", { name: `Share ${LAW_BILL.label}` });
      await share.click();
      const sheet = page.getByRole("dialog", { name: "Share this bill" });
      await expect(sheet.getByLabel("Link")).toHaveValue(new RegExp(`/bills/${LAW_BILL.id}$`));
      await expectAccessible(page, `/vote bill share sheet (${colorScheme})`);
      await page.keyboard.press("Escape");
      await expect(sheet).toBeHidden();
      await expect(page.getByRole("heading", { level: 2, name: LAW_BILL.title })).toBeVisible();
    });

    // #717: Details is a popup in the middle of the screen, and on a phone its vote buttons stay on
    // screen, so nobody has to close it to vote.
    test(`vote Details popup on a phone fits and is accessible (${colorScheme})`, async ({ page }) => {
      await page.setViewportSize({ width: 390, height: 844 });
      await regenerate(page, "/vote");
      await page.goto("/vote");
      await expect(page.getByRole("heading", { level: 1, name: "Vote" })).toBeVisible();
      await page.getByRole("button", { name: "Details" }).click();
      const details = page.getByRole("dialog", { name: LAW_BILL.title });
      await expect(details.getByRole("heading", { name: "AI summary", exact: true })).toBeVisible();
      await expect(details.getByRole("heading", { name: "Where it stands" })).toBeVisible();
      for (const box of [await details.boundingBox(), await details.getByRole("button", { name: /^Yea/ }).boundingBox()]) {
        expect(box).not.toBeNull();
        expect(box!.y).toBeGreaterThanOrEqual(0);
        expect(box!.y + box!.height).toBeLessThanOrEqual(844);
      }
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
      await expectAccessible(page, `/vote Details popup at 390px (${colorScheme})`);
    });

    // #717: the home page opens with the statement and the newest law, and fits a phone.
    test(`home page on a phone fits and is accessible (${colorScheme})`, async ({ page }) => {
      await page.setViewportSize({ width: 390, height: 844 });
      await page.goto("/");
      await expect(page.getByRole("heading", { level: 1, name: "What Congress is doing, in plain language." })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
      await expectAccessible(page, `/ at 390px (${colorScheme})`);
    });

    test(`a law's expanded summary and its text reader are accessible (${colorScheme})`, async ({ page }) => {
      await page.goto(`/bills/${LAW_BILL.id}`);
      // The note shows once the page has hydrated, so the clicks below land.
      await expect(page.getByText("Kept on this device only.")).toBeVisible();
      await page.getByRole("button", { name: "Read more" }).click();
      await expect(page.getByRole("button", { name: "Show less" })).toHaveAttribute("aria-expanded", "true");
      await expectAccessible(page, `/bills/${LAW_BILL.id} with the summary expanded (${colorScheme})`);

      await page.getByRole("tab", { name: "Text (1)" }).click();
      // A law's Text tab leads with its final text, the enrolled bill until the Public Law is printed.
      await expect(page.getByRole("button", { name: /^Final text/ })).toHaveAttribute("aria-expanded", "true");
      await expectAccessible(page, `/bills/${LAW_BILL.id} Text tab with the final text (${colorScheme})`);
      await page.getByRole("button", { name: "Read the final text" }).click();
      const reader = page.getByRole("dialog", { name: LAW_BILL.textVersion });
      await expect(reader.getByRole("heading", { name: LAW_BILL.firstSection })).toBeVisible();
      await expectAccessible(page, `/bills/${LAW_BILL.id} text reader (${colorScheme})`);
    });

    // #738: My votes empty, then with a vote, on a desktop and a phone (where it mustn't scroll sideways).
    test(`my votes, empty and with a vote, on a desktop and a phone, is accessible (${colorScheme})`, async ({ page }) => {
      // Five axe runs: past the 30s default on a busy CI runner.
      test.slow();
      await page.setViewportSize({ width: 1440, height: 900 });
      await page.goto("/my-votes");
      await expect(page.getByRole("heading", { name: "You haven't voted on any bills yet" })).toBeVisible();
      await expect(page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "My votes" })).toHaveAttribute(
        "aria-current",
        "page",
      );
      await expectAccessible(page, `/my-votes with no votes (${colorScheme})`);

      await voteYeaOnHouseBill(page);
      await page.goto("/my-votes");
      const row = page.getByRole("listitem").filter({ has: page.getByRole("link", { name: /Companion Act/ }) });
      await expect(row).toContainText("Not passed");
      await expectAccessible(page, `/my-votes with a vote at 1440px (${colorScheme})`);
      // #843: the vote buttons open under the row from Change.
      await row.getByRole("button", { name: /^Change my vote/ }).click();
      await expect(row.getByRole("button", { name: "Yea", exact: true })).toHaveAttribute("aria-pressed", "true");
      await expectAccessible(page, `/my-votes with a row open at 1440px (${colorScheme})`);

      await row.getByRole("button", { name: /^Remove my vote/ }).click();
      await expect(row.getByRole("status")).toBeVisible();
      await expectAccessible(page, `/my-votes after a removal (${colorScheme})`);
      await row.getByRole("button", { name: /^Undo/ }).click();

      await page.setViewportSize({ width: 390, height: 844 });
      await expect(row.getByRole("button", { name: "Yea", exact: true })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
      await expectAccessible(page, `/my-votes with a vote at 390px (${colorScheme})`);

      // Every control is reachable by keyboard: Tab from the summary reaches the open row's buttons.
      await page.getByRole("link", { name: "Compare with your representatives" }).focus();
      const reached: string[] = [];
      for (let i = 0; i < 10; i++) {
        await page.keyboard.press("Tab");
        reached.push(await page.evaluate(() => document.activeElement?.textContent?.trim() ?? ""));
      }
      expect(reached).toEqual(expect.arrayContaining(["Companion Act", "Done", "Yea", "Nay", "Skip", "Remove"]));
    });

    test(`a bill page on a phone, its cards folded and opened, is accessible (${colorScheme})`, async ({ page }) => {
      // #664: below the lg breakpoint the progress and action cards fold behind their titles, and the
      // progress is a vertical list of steps with their dates.
      test.slow();
      await page.setViewportSize({ width: 390, height: 844 });
      await page.goto(`/bills/${LAW_BILL.id}`);
      await expect(page.getByText("Kept on this device only.")).toBeVisible();
      const progress = page.getByRole("button", { name: /^Legislative progress/ });
      await expect(progress).toHaveAttribute("aria-expanded", "false");
      await expectAccessible(page, `/bills/${LAW_BILL.id} at 390px, cards folded (${colorScheme})`);

      await progress.click();
      await expect(progress).toHaveAttribute("aria-expanded", "true");
      await expect(page.locator('li[aria-current="step"]')).toBeVisible();
      const actions = page.getByRole("button", { name: /^Action timeline/ });
      await actions.click();
      await expect(actions).toHaveAttribute("aria-expanded", "true");
      await expectAccessible(page, `/bills/${LAW_BILL.id} at 390px, progress and actions open (${colorScheme})`);
    });

    // #858: a CRA resolution's "The rule this resolution disapproves" card (#643), matched to a Federal
    // Register document and unmatched, open on a desktop (where it starts open) and on a phone (where
    // it starts folded and the visitor opens it).
    test(`a CRA resolution's disapproved-rule card, matched and unmatched, is accessible (${colorScheme})`, async ({
      page,
    }) => {
      // Four axe runs: past the 30s default on a busy CI runner.
      test.slow();
      const cases = [
        { id: CRA_BILL.id, shows: page.getByRole("link", { name: CRA_BILL.document, exact: true }) },
        { id: CRA_UNMATCHED_BILL.id, shows: page.getByRole("link", { name: new RegExp(CRA_UNMATCHED_BILL.rule) }) },
      ];
      for (const { id, shows } of cases) {
        for (const width of [1280, 390]) {
          await page.setViewportSize({ width, height: 844 });
          await page.goto(`/bills/${id}`);
          // The note shows once the page has hydrated, so the click below lands.
          await expect(page.getByText("Kept on this device only.")).toBeVisible();
          const card = page.getByRole("button", { name: /^The rule this resolution disapproves/ });
          if (width < 1024) {
            await expect(card).toHaveAttribute("aria-expanded", "false");
            await card.click();
          }
          await expect(card).toHaveAttribute("aria-expanded", "true");
          await expect(shows).toBeVisible();
          expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
          await expectAccessible(page, `/bills/${id} with the rule card open at ${width}px (${colorScheme})`);
        }
      }
    });

    // Every page's navbar collapses into this menu below the md breakpoint (768px) (#661). The home
    // page's menu also holds its "Start voting" call to action.
    for (const path of ["/bills", "/"]) {
      test(`mobile navigation menu on ${path} is accessible (${colorScheme})`, async ({ page }) => {
        await page.setViewportSize({ width: 390, height: 844 });
        await page.goto(path);
        const menu = page.getByRole("button", { name: "Toggle navigation menu" });
        await menu.click();
        await expect(menu).toHaveAttribute("aria-expanded", "true");
        await expectAccessible(page, `mobile navigation menu on ${path} (${colorScheme})`);
      });
    }
  });
}
