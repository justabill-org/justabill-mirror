/**
 * The id of every page's `<main>`, the skip link's target. Each `<main>` also takes
 * `tabIndex={-1}`, so following the link moves focus into it, not just the scroll position.
 */
export const MAIN_ID = "main";

/**
 * "Skip to main content", the first thing Tab reaches on every page (#955, after USWDS's skip-nav).
 * Hidden until it has focus; Enter moves focus to the page's `<main id="main" tabIndex={-1}>`.
 * Its text is --foreground on --background and its outline --ring, both in contrast.test.ts.
 */
export function SkipLink() {
  return (
    <a
      href={`#${MAIN_ID}`}
      className="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-3 focus:z-[60] focus:rounded-lg focus:border focus:border-border focus:bg-background focus:px-4 focus:py-2 focus:text-sm focus:font-medium focus:text-foreground focus:shadow-md"
    >
      Skip to main content
    </a>
  );
}
