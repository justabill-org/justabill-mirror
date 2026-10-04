// Trust pages (#75): the links every footer carries, where to write to us, and the Congress.gov
// link that sits under each AI summary.

import type { Metadata } from "next";
import type { Amendment, Bill } from "@/lib/types";

/** The trust pages, in footer order. */
export const TRUST_LINKS = [
  { href: "/about", label: "About" },
  { href: "/methodology", label: "Methodology" },
  { href: "/privacy", label: "Privacy" },
  { href: "/terms", label: "Terms" },
  { href: "/contact", label: "Contact" },
] as const;

/** Title, description and canonical URL for a trust page. */
export function trustMetadata(path: TrustPath, title: string, description: string): Metadata {
  return {
    title: `${title} | Just a Bill`,
    description,
    alternates: { canonical: path },
    openGraph: { title: `${title} | Just a Bill`, description, url: path, type: "website" },
  };
}

export type TrustPath = (typeof TRUST_LINKS)[number]["href"];

/**
 * What the site says about the code while the repo is private: no date, and no link (#806). The code
 * goes public through a mirror of each release (#805), which decides what the pages say then.
 */
export const CODE_PUBLICATION = "We'll publish the code under the Apache-2.0 license.";

/** General questions by email (Google Workspace on justabill.io, #368). */
export const CONTACT_EMAIL = "contact@justabill.io";

/** Privacy questions and requests by email (#368). */
export const PRIVACY_EMAIL = "privacy@justabill.io";

/** The date shown at the top of /privacy and /terms. Change it with any change to either page. */
export const POLICIES_LAST_UPDATED = "October 4, 2026";

/**
 * The policies' change history, newest first, listed at the end of /privacy and /terms (#806). The two
 * share POLICIES_LAST_UPDATED, so they share the list: each entry names the policy that changed. Only
 * significant changes get an entry (wording tweaks don't); add one dated POLICIES_LAST_UPDATED with each.
 */
export const POLICY_CHANGES: readonly { date: string; change: string }[] = [
  {
    date: "October 4, 2026",
    change:
      "Privacy Policy: the Vote page no longer saves the filter you pick in your browser; the filter is part of " +
      "the page's address instead.",
  },
  {
    date: "October 3, 2026",
    change:
      "Privacy Policy: questions, problem reports and privacy requests now go to contact@justabill.io and " +
      "privacy@justabill.io instead of GitHub, and Google Workspace holds that email.",
  },
  {
    date: "October 3, 2026",
    change:
      "Terms of Service: the Terms no longer point to our code on GitHub. We'll publish the code under the " +
      "Apache-2.0 license, which will govern its use.",
  },
];

/** Basecamp's policies, which /privacy and /terms adapt under CC BY 4.0. */
export const BASECAMP_POLICIES_URL = "https://github.com/basecamp/policies";
export const CC_BY_URL = "https://creativecommons.org/licenses/by/4.0/";

// Congress.gov's URL slug for each bill type.
const CONGRESS_GOV_TYPES: Record<string, string> = {
  hr: "house-bill",
  s: "senate-bill",
  hjres: "house-joint-resolution",
  sjres: "senate-joint-resolution",
  hconres: "house-concurrent-resolution",
  sconres: "senate-concurrent-resolution",
  hres: "house-resolution",
  sres: "senate-resolution",
};

/** 119 → "119th", 101 → "101st", 112 → "112th". */
export function ordinal(n: number): string {
  const mod100 = n % 100;
  if (mod100 >= 11 && mod100 <= 13) return `${n}th`;
  switch (n % 10) {
    case 1:
      return `${n}st`;
    case 2:
      return `${n}nd`;
    case 3:
      return `${n}rd`;
    default:
      return `${n}th`;
  }
}

/**
 * The bill's official text on Congress.gov, e.g.
 * https://www.congress.gov/bill/119th-congress/house-bill/1/text, or undefined for a bill
 * type Congress.gov doesn't list.
 */
export function congressGovTextUrl(bill: Pick<Bill, "congress" | "bill_type" | "number">): string | undefined {
  const type = CONGRESS_GOV_TYPES[bill.bill_type];
  if (!type || !(bill.congress > 0) || !(bill.number > 0)) return undefined;
  return `https://www.congress.gov/bill/${ordinal(bill.congress)}-congress/${type}/${bill.number}/text`;
}

// Congress.gov's URL slug for each amendment type. Senate unprinted amendments (suamdt) have no
// page of this form, so they get no link.
const CONGRESS_GOV_AMENDMENT_TYPES: Record<string, string> = {
  samdt: "senate-amendment",
  hamdt: "house-amendment",
};

/**
 * The amendment's page on Congress.gov, e.g.
 * https://www.congress.gov/amendment/119th-congress/senate-amendment/12, or undefined for an
 * amendment type Congress.gov doesn't list that way.
 */
export function congressGovAmendmentUrl(
  amendment: Pick<Amendment, "congress" | "amendment_type" | "amendment_number">,
): string | undefined {
  const type = CONGRESS_GOV_AMENDMENT_TYPES[amendment.amendment_type.toLowerCase()];
  if (!type || !(amendment.congress > 0) || !(amendment.amendment_number > 0)) return undefined;
  return `https://www.congress.gov/amendment/${ordinal(amendment.congress)}-congress/${type}/${amendment.amendment_number}`;
}
