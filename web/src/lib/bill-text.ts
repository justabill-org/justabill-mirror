import type { BillTextSection } from "@/lib/types";

/** How much of a unit's text labels it in the contents when it has no designation or header. */
const LABEL_CHARS = 60;

/**
 * The heading a bill text unit prints with: "Sec. 101. Short title", "Title I—Agriculture",
 * "(a) In general". Designations ending in "." or ")" are followed by a space, the rest by a dash,
 * as in print. It's empty when the unit has neither.
 */
export function sectionHeading(section: BillTextSection): string {
  const enumText = section.enum?.trim() ?? "";
  const header = section.header.trim();
  if (!enumText || !header) return enumText || header;
  return /[.)]$/.test(enumText) ? `${enumText} ${header}` : `${enumText}—${header}`;
}

/** The unit's label in the contents: its heading, or the start of its text. */
export function contentsLabel(section: BillTextSection): string {
  const heading = sectionHeading(section);
  if (heading) return heading;
  const text = section.content.trim().replace(/\s+/g, " ");
  return text.length > LABEL_CHARS ? `${text.slice(0, LABEL_CHARS)}…` : text;
}

/**
 * Whether the contents lists a unit's children. A section's subsections are left out: the
 * contents of a large bill (the NDAA has about a thousand sections) would otherwise run to
 * thousands of lines.
 */
export function listsChildrenInContents(section: BillTextSection): boolean {
  return section.kind !== "section" && section.kind !== "subsection";
}
