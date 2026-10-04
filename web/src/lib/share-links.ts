// What the share dialog sends (#88, docs/design/88-share-cards.md, item 2): the share text, the
// intent links and the file name, built only from what the card prints. Pure functions, so the
// dialog, the buttons and the tests agree on every string that leaves the browser. A bill's own
// link (#810) holds only the bill: its page and its number and title.

import {
  AGGREGATE_CARD_LABEL,
  aggregatePlace,
  parseBillId,
  type PublishedCell,
  type ShareVote,
} from "./share";
import { truncate } from "./bill-metadata";
import { siteUrl } from "./site";
import { BILL_TYPE_LABELS } from "./types";

/** What was shared: a card (a vote on a bill, an aggregate cell) or a bill's own page. */
export type ShareKind = "bill" | "aggregate" | "link";

/** How a card or link was shared, for the `share` analytics event. */
export type ShareChannel =
  | "native"
  | "copy"
  | "download"
  | "x"
  | "facebook"
  | "bluesky"
  | "threads"
  | "reddit"
  | "whatsapp"
  | "linkedin"
  | "email";

export interface IntentLink {
  channel: Exclude<ShareChannel, "native" | "copy" | "download">;
  label: string;
  href: string;
}

/** What the browser can share natively: the card image as a file, only the link, or nothing. */
export type ShareSupport = "files" | "url" | "none";

/** The navigator methods the dialog uses, so tests can pass their own. */
export interface ShareNavigator {
  share?: (data: ShareData) => Promise<void>;
  canShare?: (data: ShareData) => boolean;
}

/** The share page as an absolute URL on `origin`. */
export function absoluteShareUrl(path: string, origin: string): string {
  return new URL(path, origin).toString();
}

/** "H.R. 1" for "hr-119-1", or the ID itself if it isn't a canonical bill ID. */
export function billLabel(billId: string): string {
  const parsed = parseBillId(billId);
  if (!parsed) return billId;
  return `${BILL_TYPE_LABELS[parsed.billType]} ${parsed.number}`;
}

/** A bill's share text is cut here, so it and the link fit in one post on X (280, a link counts 23). */
export const SHARE_TEXT_MAX = 200;

/** The bill's own page, "/bills/hr-119-1": the same path as its canonical URL and og:url. */
export function billLinkPath(billId: string): string {
  return `/bills/${encodeURIComponent(billId)}`;
}

/**
 * The bill's page as an absolute URL, on NEXT_PUBLIC_SITE_URL when it's set (as the page's og:url
 * is) and on `origin` otherwise.
 */
export function billLinkUrl(billId: string, origin: string, site: URL | undefined = siteUrl()): string {
  return absoluteShareUrl(billLinkPath(billId), site?.origin ?? origin);
}

/** "H.R. 1: One Big Bill", cut at a word boundary to SHARE_TEXT_MAX characters. */
export function billLinkText(billId: string, title: string): string {
  const label = billLabel(billId);
  return title.trim() ? truncate(`${label}: ${title}`, SHARE_TEXT_MAX) : label;
}

/** "I'd vote Yea on H.R. 1." */
export function billShareText(billId: string, vote: ShareVote): string {
  return `I'd vote ${vote === "yea" ? "Yea" : "Nay"} on ${billLabel(billId)}.`;
}

/** "Just a Bill users in CA-12: 62% Yea, 38% Nay on H.R. 1 (Just a Bill users, not a poll)." */
export function aggregateShareText(
  billId: string,
  cell: Pick<PublishedCell, "scope_key" | "yea_pct" | "nay_pct">
): string {
  const place = aggregatePlace(cell.scope_key);
  return (
    `Just a Bill users ${place}: ${cell.yea_pct}% Yea, ${cell.nay_pct}% Nay on ${billLabel(billId)} ` +
    `(${AGGREGATE_CARD_LABEL}).`
  );
}

/**
 * Plain links to each site's own share page: no SDKs or scripts, so the CSP stays as it is. Sites
 * that take only one field get the text and the link together.
 */
export function intentLinks(url: string, text: string): IntentLink[] {
  const u = encodeURIComponent(url);
  const t = encodeURIComponent(text);
  const both = encodeURIComponent(`${text} ${url}`);
  return [
    { channel: "x", label: "X", href: `https://x.com/intent/post?text=${t}&url=${u}` },
    { channel: "facebook", label: "Facebook", href: `https://www.facebook.com/sharer/sharer.php?u=${u}` },
    { channel: "bluesky", label: "Bluesky", href: `https://bsky.app/intent/compose?text=${both}` },
    { channel: "threads", label: "Threads", href: `https://www.threads.com/intent/post?text=${t}&url=${u}` },
    { channel: "reddit", label: "Reddit", href: `https://www.reddit.com/submit?url=${u}&title=${t}` },
    { channel: "whatsapp", label: "WhatsApp", href: `https://wa.me/?text=${both}` },
    { channel: "linkedin", label: "LinkedIn", href: `https://www.linkedin.com/sharing/share-offsite/?url=${u}` },
    {
      channel: "email",
      label: "Email",
      href: `mailto:?subject=${encodeURIComponent("Just a Bill")}&body=${encodeURIComponent(`${text}\n\n${url}`)}`,
    },
  ];
}

/** Whether `nav` can share `file` (when there is one) or at least a link. */
export function shareSupport(nav: ShareNavigator | undefined, file: File | null): ShareSupport {
  if (typeof nav?.share !== "function") return "none";
  if (file && typeof nav.canShare === "function") {
    try {
      if (nav.canShare({ files: [file] })) return "files";
    } catch {
      // Some browsers throw on file data they don't support; fall back to the link.
    }
  }
  return "url";
}

/** "just-a-bill-bill-hr-119-1-yea.png" for "/share/bill/hr-119-1/yea". */
export function shareFileName(path: string): string {
  const slug = path
    .replace(/^\/share\//, "")
    .replace(/[^A-Za-z0-9-]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return `just-a-bill-${slug}.png`;
}

/** True when the visitor closed the share sheet, which isn't an error worth showing. */
export function isAbortError(err: unknown): boolean {
  // A DOMException, which isn't an Error subclass everywhere, so check the name alone.
  return typeof err === "object" && err !== null && (err as { name?: unknown }).name === "AbortError";
}
