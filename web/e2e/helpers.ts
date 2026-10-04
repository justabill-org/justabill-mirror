// What the smoke tests expect to find. The rows come from db/fixture (seeded by db/cmd/e2e-seed),
// and none of these values appear in src/lib/examples, so a page that fell back to the bundled
// example JSON can't pass a test that asserts on them (docs/design/84-e2e-smoke-tests.md).
// Keep them in step with db/fixture/fixture.go.

import type { Locator, Page } from "@playwright/test";
import { EXPERIMENTS } from "../src/lib/experiments/registry";

/** The title HR 1 and S 1 of the 119th Congress share. */
export const COMPANION_TITLE = "Companion Act";

/** HR 1 of the 119th: sponsored by Ada Alvarez, with one House roll call. */
export const HOUSE_BILL = {
  id: "hr-119-1",
  label: "H.R. 1",
  sponsor: { name: "Ada Alvarez", bioguideId: "A000001" },
  rollCalls: 1,
} as const;

/** S 1 of the 119th: the same number and title as HR 1, a different bill. */
export const SENATE_BILL = {
  id: "s-119-1",
  label: "S. 1",
} as const;

/**
 * HR 808 of the 119th: it became law, and has an AI summary (with a long summary behind "Read
 * more") and one text version, the enrolled bill, with parsed sections. It's the only law, so the
 * only card /vote offers.
 */
export const LAW_BILL = {
  id: "hr-119-808",
  label: "H.R. 808",
  title: "Lamplight Library Hours Act",
  textVersion: "Enrolled Bill",
  firstSection: "101. Short title",
  /** The last entry in its text's contents (#883). */
  lastSection: "102. Evening hours grants",
} as const;

/**
 * S.J.Res. 41 of the 119th, a Congressional Review Act resolution whose rule is matched by its
 * citation to a Federal Register document, so its page shows that document in the "The rule this
 * resolution disapproves" card (#643). Not law, so never on /vote's default deck.
 */
export const CRA_BILL = {
  id: "sjres-119-41",
  document: "Lantern Wick Efficiency Standards",
  citation: "90 FR 91234",
} as const;

/**
 * H.J.Res. 63 of the 119th, a CRA resolution that names its rule by a GAO opinion and wasn't
 * matched: its card says so and links a Federal Register search for the rule.
 */
export const CRA_UNMATCHED_BILL = {
  id: "hjres-119-63",
  rule: "Guidance on Reading Room Hours",
} as const;

/**
 * The House member for CA-12, where the Census stub (e2e/stubs/census.mjs) puts every address.
 * She voted Yea on HR 1's one roll call.
 */
export const CA12_REP = {
  name: "Ada Alvarez",
  bioguideId: "A000001",
  state: "CA",
  district: 12,
  voteOnHouseBill: "yea",
} as const;

/** Members the fixture seats outside CA-12 (TX-7 and an Ohio senator): never the visitor's reps. */
export const OTHER_MEMBERS = ["Ben Brooks", "Cora Chen"] as const;

/** Any address will do: the stub answers CA-12 for all of them. The form takes it in four fields. */
export const STUB_ADDRESS_FIELDS = { street: "1 E2E Way", city: "San Francisco", state: "CA", zip: "94103" } as const;

/** The one line the form sends for STUB_ADDRESS_FIELDS ("street, city, ST zip"). */
export const STUB_ADDRESS = "1 E2E Way, San Francisco, CA 94103";

/** Any point will do too: the stub answers CA-12 for every one ("Use my location"). */
export const STUB_LOCATION = { latitude: 37.7749, longitude: -122.4194, accuracy: 25 } as const;

/**
 * The browser's localStorage keys for the visitor's votes and reps (src/lib/local). Written out
 * rather than imported: returning visitors' votes live under these names, so a rename must fail
 * a test.
 */
export const VOTES_KEY = "jab.votes.v1";
export const REPS_KEY = "jab.reps.v1";

/**
 * The time the e2e web server judges experiments at (JAB_EXPERIMENTS_NOW, playwright.config.ts):
 * noon UTC on the first registry experiment's first day, so its proxy assigns arms whatever day
 * the tests run. Empty, the real clock, when no experiment is in the registry.
 */
export const EXPERIMENTS_NOW = EXPERIMENTS.length > 0 ? `${EXPERIMENTS[0].start}T12:00:00Z` : "";

/**
 * A bill's card on a list page (/bills, the home page): the list item in `main` whose title links
 * the bill (#811), visible only. While React streams a list in, a hidden copy of each card is in
 * the DOM too, so a page-wide match can find two elements and fail strict mode (#792).
 */
export function billCard(page: Page, id: string): Locator {
  return page
    .getByRole("main")
    .getByRole("listitem")
    .filter({ has: page.locator(`a[href="/bills/${id}"]`), visible: true });
}
