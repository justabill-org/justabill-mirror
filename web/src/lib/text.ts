// Plain-text helpers with no framework imports, so the phone app's shared modules (share-links.ts,
// reached from my-votes.ts) can use them without pulling in Next.js (#1093).

/** Shortens text to at most `max` characters at a word boundary, with an ellipsis. */
export function truncate(text: string, max: number): string {
  const clean = text.replace(/\s+/g, " ").trim();
  if (clean.length <= max) return clean;
  // Room for the ellipsis. The cut ends on a word boundary if the next character is a space.
  const cut = clean.slice(0, max - 1);
  const space = clean[max - 1] === " " ? cut.length : cut.lastIndexOf(" ");
  return `${(space > max / 2 ? cut.slice(0, space) : cut).replace(/[\s,;:.]+$/, "")}…`;
}
