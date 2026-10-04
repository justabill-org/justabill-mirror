import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

// WCAG 2.2 AA contrast for both palettes in globals.css (#83, #335). axe can't check colors in
// jsdom, so this checks the token pairs the components actually put together: text tokens on
// the surfaces they sit on and on their own 10% tints (Badge, the vote buttons), and the
// *-foreground text on filled buttons and chips.

const css = readFileSync(path.resolve(__dirname, "../../app/globals.css"), "utf8");

type Palette = Record<string, string>;

/** The hex custom properties declared in a CSS block's body. */
function hexTokens(block: string): Palette {
  const tokens: Palette = {};
  for (const [, name, hex] of block.matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{6})\s*;/g)) {
    tokens[name] = hex.toLowerCase();
  }
  return tokens;
}

/** The light palette: the first top-level `:root { … }` block. */
function lightTokens(): Palette {
  return hexTokens(/^:root\s*{([^}]*)}/m.exec(css)?.[1] ?? "");
}

/** The dark palette: the light one with the `prefers-color-scheme: dark` overrides on top. */
function darkTokens(): Palette {
  const block = /@media \(prefers-color-scheme: dark\)\s*{\s*:root\s*{([^}]*)}/.exec(css)?.[1] ?? "";
  return { ...lightTokens(), ...hexTokens(block) };
}

/** `fg` at `alpha` over an opaque `bg`, as Tailwind's `bg-x/10` composites in sRGB. */
function over(fg: string, alpha: number, bg: string): string {
  const channel = (hex: string, i: number) => parseInt(hex.slice(i, i + 2), 16);
  return `#${[1, 3, 5]
    .map((i) => Math.round(alpha * channel(fg, i) + (1 - alpha) * channel(bg, i)))
    .map((c) => c.toString(16).padStart(2, "0"))
    .join("")}`;
}

function luminance(hex: string): number {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrastRatio(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

const SURFACES = ["background", "card"];
const TEXT_ON_SURFACES = [
  "foreground",
  "muted-foreground",
  "secondary-foreground",
  "link",
  "success",
  "destructive",
  "vote-yea",
  "vote-nay",
  "vote-skip",
  "party-d",
  "party-r",
  "party-i",
];
// Colors drawn as text on their own tint: `bg-x/10 text-x` (Badge variants, the unpressed
// vote buttons, icon tiles). 10% is the darkest tint in use, so it's the worst case.
const TEXT_ON_OWN_TINT = [
  "link",
  "success",
  "destructive",
  "vote-yea",
  "vote-nay",
  "vote-skip",
  "party-d",
  "party-r",
  "party-i",
];
const TINT = 0.1;
// [text, fill]: text drawn on a filled token. Only neutral text sits on --muted (inactive
// filter chips, the tab list, secondary buttons).
const TEXT_ON_FILLS: [string, string][] = [
  ["foreground", "muted"],
  ["muted-foreground", "muted"],
  ["secondary-foreground", "muted"],
  ["primary-foreground", "primary"],
  ["secondary-foreground", "secondary"],
  ["success-foreground", "success"],
  ["destructive-foreground", "destructive"],
  ["vote-yea-foreground", "vote-yea"],
  ["vote-nay-foreground", "vote-nay"],
  ["vote-skip-foreground", "vote-skip"],
];
// Fills drawn at 90% over a card: the swipe overlays and the hover state of filled buttons.
const FILL_ALPHA = 0.9;

const AA_NORMAL_TEXT = 4.5;
// [graphic, track]: bars drawn on a track, WCAG 1.4.11's 3:1 for graphical objects. The
// scorecard's "Voted with you" bar is the match green on --muted (#844).
const GRAPHICS_ON_TRACKS: [string, string][] = [["success", "muted"]];
const AA_NON_TEXT = 3;

it("matches known ratios", () => {
  expect(contrastRatio("#000000", "#ffffff")).toBeCloseTo(21, 5);
  expect(contrastRatio("#d97706", "#fafafa")).toBeCloseTo(3.05, 2);
  // #335's example: red-600 on its 10% tint over the light background.
  expect(contrastRatio("#dc2626", over("#dc2626", TINT, "#fafafa"))).toBeCloseTo(3.98, 2);
});

describe.each([
  ["light", lightTokens()],
  ["dark", darkTokens()],
])("%s palette contrast (WCAG AA, normal text)", (_, tokens) => {
  const color = (name: string) => tokens[name];

  it("reads the palette", () => {
    for (const name of [...SURFACES, ...TEXT_ON_SURFACES, ...TEXT_ON_FILLS.flat()]) {
      expect(tokens[name], `--${name}`).toMatch(/^#[0-9a-f]{6}$/);
    }
  });

  for (const text of TEXT_ON_SURFACES) {
    for (const surface of SURFACES) {
      it(`--${text} on --${surface}`, () => {
        expect(contrastRatio(color(text), color(surface))).toBeGreaterThanOrEqual(AA_NORMAL_TEXT);
      });
    }
  }

  for (const text of TEXT_ON_OWN_TINT) {
    for (const surface of SURFACES) {
      it(`--${text} on its ${TINT * 100}% tint over --${surface}`, () => {
        const tint = over(color(text), TINT, color(surface));
        expect(contrastRatio(color(text), tint)).toBeGreaterThanOrEqual(AA_NORMAL_TEXT);
      });
    }
  }

  for (const [graphic, track] of GRAPHICS_ON_TRACKS) {
    it(`--${graphic} bar on a --${track} track`, () => {
      expect(contrastRatio(color(graphic), color(track))).toBeGreaterThanOrEqual(AA_NON_TEXT);
    });
  }

  for (const [text, fill] of TEXT_ON_FILLS) {
    it(`--${text} on --${fill}`, () => {
      expect(contrastRatio(color(text), color(fill))).toBeGreaterThanOrEqual(AA_NORMAL_TEXT);
    });
    it(`--${text} on --${fill} at ${FILL_ALPHA * 100}% over --card`, () => {
      const fillOverCard = over(color(fill), FILL_ALPHA, color("card"));
      expect(contrastRatio(color(text), fillOverCard)).toBeGreaterThanOrEqual(AA_NORMAL_TEXT);
    });
  }
});
