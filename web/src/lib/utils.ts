import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

const DATE_STYLES = {
  /** Jan 3, 2025 */
  medium: { month: "short", day: "numeric", year: "numeric" },
  /** January 3, 2025 */
  long: { month: "long", day: "numeric", year: "numeric" },
  /** Jan 3 */
  short: { month: "short", day: "numeric" },
  /** Friday, January 3, 2025 */
  weekday: { weekday: "long", month: "long", day: "numeric", year: "numeric" },
} satisfies Record<string, Intl.DateTimeFormatOptions>;

export type DateStyle = keyof typeof DATE_STYLES;

/**
 * An API date as text, always read in UTC. The API sends calendar dates (introduced, status,
 * action, text version and House vote dates) as UTC midnight and Senate vote times as the clerk's
 * Eastern clock time marked UTC, so formatting in the reader's zone shows the day before
 * everywhere in the US (#454). UTC also makes the server and browser renders agree, so a client
 * component doesn't hydrate with a different date.
 */
export function formatDate(date: string | Date, style: DateStyle = "medium"): string {
  return new Date(date).toLocaleDateString("en-US", { ...DATE_STYLES[style], timeZone: "UTC" });
}

export function getRelativeTime(date: string | Date): string {
  const now = new Date();
  const then = new Date(date);
  const diffInSeconds = Math.floor((now.getTime() - then.getTime()) / 1000);

  if (diffInSeconds < 60) return "just now";
  if (diffInSeconds < 3600) return `${Math.floor(diffInSeconds / 60)}m ago`;
  if (diffInSeconds < 86400) return `${Math.floor(diffInSeconds / 3600)}h ago`;
  if (diffInSeconds < 604800) return `${Math.floor(diffInSeconds / 86400)}d ago`;
  return formatDate(date, "short");
}

export function truncate(str: string, length: number): string {
  if (str.length <= length) return str;
  return str.slice(0, length) + "...";
}

const HTML_ENTITIES: Record<string, string> = {
  "&lt;": "<",
  "&gt;": ">",
  "&amp;": "&",
  "&quot;": '"',
  "&#39;": "'",
  "&nbsp;": " ",
};

/**
 * The plain text of an HTML fragment, for rendering as text. In the browser DOMParser does it.
 * On the server it strips tags first (an unclosed one runs to the end) and then decodes the
 * common entities in one pass, so an escaped `&lt;script&gt;` comes out as the text `<script>`
 * and no tag survives (#386).
 */
export function htmlToText(html: string): string {
  if (typeof document !== "undefined") {
    const doc = new DOMParser().parseFromString(html, "text/html");
    return doc.documentElement.textContent?.trim() ?? html;
  }
  return html
    .replace(/<[^>]*(?:>|$)/g, "")
    .replace(/&(?:lt|gt|amp|quot|#39|nbsp);/g, (entity) => HTML_ENTITIES[entity] ?? entity)
    .trim();
}
