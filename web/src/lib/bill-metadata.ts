import type { Metadata } from "next";
import { BILL_TYPE_LABELS, type BillDetailResponse } from "./types";

const SITE_NAME = "Just a Bill";
/** Search engines show about 155–160 characters of a description. */
const MAX_DESCRIPTION = 160;
/** Titles past about 70 characters are cut off in search results. */
const MAX_TITLE = 90;

/** "H.R. 1" for a bill of type hr and number 1. */
export function billLabel(billType: string, number: number): string {
  return `${BILL_TYPE_LABELS[billType] ?? billType.toUpperCase()} ${number}`;
}

/** Shortens text to at most `max` characters at a word boundary, with an ellipsis. */
export function truncate(text: string, max: number): string {
  const clean = text.replace(/\s+/g, " ").trim();
  if (clean.length <= max) return clean;
  // Room for the ellipsis. The cut ends on a word boundary if the next character is a space.
  const cut = clean.slice(0, max - 1);
  const space = clean[max - 1] === " " ? cut.length : cut.lastIndexOf(" ");
  return `${(space > max / 2 ? cut.slice(0, space) : cut).replace(/[\s,;:.]+$/, "")}…`;
}

/**
 * The bill page's title, description and link-preview tags: the bill's number and title, and
 * its plain-language summary (or latest action) as the description.
 */
export function billMetadata({ bill, summary }: BillDetailResponse): Metadata {
  const label = billLabel(bill.bill_type, bill.number);
  const heading = truncate(`${label}: ${bill.title}`, MAX_TITLE);
  const description = truncate(
    summary?.short_summary ||
      (bill.latest_action?.text ? `Latest action: ${bill.latest_action.text}` : "") ||
      `${label} in the ${bill.congress}th Congress. Read it in plain language and vote on it.`,
    MAX_DESCRIPTION
  );
  const url = `/bills/${encodeURIComponent(bill.id)}`;
  return {
    title: `${heading} | ${SITE_NAME}`,
    description,
    alternates: { canonical: url },
    openGraph: { title: heading, description, type: "article", url, siteName: SITE_NAME },
    twitter: { card: "summary_large_image", title: heading, description },
  };
}

/** Metadata when the bill can't be read: a plain title, and kept out of search results. */
export const unavailableBillMetadata: Metadata = {
  title: `Bill | ${SITE_NAME}`,
  robots: { index: false, follow: true },
};
