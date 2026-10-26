import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import PrivacyPage from "@/app/(public)/(trust)/privacy/page";
import TermsPage from "@/app/(public)/(trust)/terms/page";
import { htmlToText } from "@/lib/utils";

// #982: /privacy must disclose everything an account stores and every note we keep in the browser. Each
// column of the user tables in db/schema.sql and each "jab.*" storage key in web/src maps to a phrase the
// rendered page must contain. A PR that adds a column or a key adds its entry here, with the page's line.

/** A phrase of /privacy for each column of users, user_votes and user_favorites, keyed "<table>.<column>". */
export const COLUMN_DISCLOSURES: Record<string, string> = {
  "users.user_id": "a random account ID",
  "users.auth_uid": "the ID Identity Platform gives you",
  "users.state": "your state and congressional district",
  "users.district": "your state and congressional district",
  "users.created_at": "when you created your account",
  "users.sign_in_provider": "which kind of account you signed in with",
  "users.district_changed_at": "when you last changed your district",
  "users.agg_excluded_at":
    "whether, and when, we set your account's votes aside from the published totals. We do that only for a " +
    "batch of accounts that look automated, picked by sign-in method and when they were created, never by how " +
    "they voted",
  "users.agg_opted_out_at": "whether, and when, you chose in Settings to leave your votes out of the published totals",
  "user_votes.user_id": "your votes on bills",
  "user_votes.bill_id": "your votes on bills",
  "user_votes.vote": "your votes on bills (yea, nay or skip)",
  "user_votes.voted_at": "when you cast each one",
  "user_votes.app_check_ok": "whether it passed our automated check that requests come from our own app",
  "user_votes.recorded_at": "when our server recorded it",
  "user_favorites.user_id": "the bills you follow",
  "user_favorites.bill_id": "the bills you follow",
  "user_favorites.created_at": "when you followed them",
};

/**
 * A phrase of /privacy for each localStorage or sessionStorage key the site uses. Transient keys point at the
 * sentence that already covers them: the storage probe and the `.corrupt` copies of an unreadable store are
 * local storage the votes and reps sentence describes, and the old /vote filter keys are removed on the
 * first visit, as the change list says.
 */
export const STORAGE_KEY_DISCLOSURES: Record<string, string> = {
  "jab.votes.v1": "Your votes (and the title of each bill you voted on) are saved in your browser's local storage",
  "jab.votes.corrupt": "Your votes (and the title of each bill you voted on) are saved in your browser's local storage",
  "jab.reps.v1": "So are your state, district and the list of your representatives",
  "jab.reps.corrupt": "So are your state, district and the list of your representatives",
  "jab.probe": "saved in your browser's local storage, on your device only",
  "jab.exp.v1": "a note that your browser has already told us it saw the page or took the action we're comparing",
  "jab.signed-in.v1": "that someone signed in on this device, so the sign-in library loads only after a sign-in",
  "jab.sign-in-redirect.v1": "the sign-in library keeps you signed in using your browser's storage",
  "jab.onboarded": "which account (by its random ID) finished setting up here, so we don't ask again",
  "jab.vote-filter.v1": "the Vote page no longer saves the filter you pick in your browser",
  "jab.vote-filter.corrupt": "the Vote page no longer saves the filter you pick in your browser",
};

const REPO = path.resolve(__dirname, "../../../..");
const WEB_SRC = path.join(REPO, "web", "src");
const USER_TABLES = ["users", "user_votes", "user_favorites"];

/** "<table>.<column>" for every column of the user tables in db/schema.sql. */
function userTableColumns(schema: string): string[] {
  return USER_TABLES.flatMap((table) => {
    const body = new RegExp(String.raw`^CREATE TABLE ${table} \(\n([\s\S]*?)\n\) PRIMARY KEY`, "m").exec(schema);
    if (!body) throw new Error(`db/schema.sql has no CREATE TABLE ${table}`);
    return body[1].split("\n").map((line) => `${table}.${line.trim().split(/\s+/)[0]}`);
  });
}

/** Source files under web/src, without tests. */
function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) return entry.name === "__tests__" || entry.name === "test" ? [] : sourceFiles(full);
    return /\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name) ? [full] : [];
  });
}

/** Every quoted "jab.*" key in web/src, with the files that use it. */
function storageKeys(): Map<string, string[]> {
  const keys = new Map<string, string[]>();
  for (const file of sourceFiles(WEB_SRC)) {
    for (const m of readFileSync(file, "utf8").matchAll(/["'`](jab\.[\w.-]+)["'`]/g)) {
      keys.set(m[1], [...(keys.get(m[1]) ?? []), path.relative(WEB_SRC, file)]);
    }
  }
  return keys;
}

/** A page's text. htmlToText leaves React's &#x27; for an apostrophe, and JSX line breaks leave runs of spaces. */
function renderedText(page: () => React.ReactElement): string {
  return htmlToText(renderToStaticMarkup(createElement(page)))
    .replace(/&#x27;/g, "'")
    .replace(/\s+/g, " ");
}

const pageText = renderedText(PrivacyPage);

const columns = userTableColumns(readFileSync(path.join(REPO, "db", "schema.sql"), "utf8"));
const keys = storageKeys();

describe("/privacy discloses every stored account field (#982)", () => {
  it("finds the user tables' columns", () => {
    expect(columns).toContain("users.agg_opted_out_at");
    expect(columns).toContain("user_votes.recorded_at");
  });

  it.each(columns)("%s has an entry in COLUMN_DISCLOSURES", (column) => {
    expect(COLUMN_DISCLOSURES, `add ${column} to COLUMN_DISCLOSURES and its line to /privacy`).toHaveProperty([
      column,
    ]);
  });

  it("maps no column the schema no longer has", () => {
    expect(Object.keys(COLUMN_DISCLOSURES).filter((c) => !columns.includes(c))).toEqual([]);
  });

  it.each(Object.entries(COLUMN_DISCLOSURES))("/privacy describes %s", (_column, phrase) => {
    expect(pageText).toContain(phrase);
  });
});

describe("/privacy discloses every note kept in the browser (#982)", () => {
  it("finds the site's storage keys", () => {
    expect([...keys.keys()]).toContain("jab.onboarded");
    expect([...keys.keys()]).toContain("jab.votes.v1");
  });

  it.each([...keys.keys()])("%s has an entry in STORAGE_KEY_DISCLOSURES", (key) => {
    expect(
      STORAGE_KEY_DISCLOSURES,
      `add ${key} (${keys.get(key)?.join(", ")}) to STORAGE_KEY_DISCLOSURES and /privacy`,
    ).toHaveProperty([key]);
  });

  it("maps no key the site no longer uses", () => {
    expect(Object.keys(STORAGE_KEY_DISCLOSURES).filter((k) => !keys.has(k))).toEqual([]);
  });

  it.each(Object.entries(STORAGE_KEY_DISCLOSURES))("/privacy describes %s", (_key, phrase) => {
    expect(pageText).toContain(phrase);
  });

  it("says sign-out removes both notes kept after a sign-in", () => {
    expect(pageText).toContain("We remove both when you sign out.");
  });
});

describe("/privacy says what signed-in votes are used for (#982)", () => {
  it("never says votes serve only the voter, or that each vote is saved by choice", () => {
    expect(pageText).not.toContain("use them only to provide the Service");
    expect(pageText).not.toContain("choose to save them");
  });

  it("says signed-in votes count toward the published totals unless the user turns that off", () => {
    expect(pageText).toContain(
      "your votes count toward the rounded totals we publish unless you turn that off in Settings",
    );
    expect(pageText).toContain("Right to limit use.");
  });

  it("names the Settings switch by its label", () => {
    const label = "Count my votes in the published totals";
    expect(readFileSync(path.join(WEB_SRC, "app", "(app)", "settings", "count-in-totals.tsx"), "utf8")).toContain(label);
    expect(pageText).toContain(`with “${label}” in Settings`);
  });

  it("the Terms license covers the published totals", () => {
    const terms = renderedText(TermsPage);
    expect(terms).toContain(
      "to provide the Service to you and, if you sign in, in the rounded, de-identified totals of how users voted",
    );
    expect(terms).not.toContain("in order to provide the Service to you, but");
  });
});
