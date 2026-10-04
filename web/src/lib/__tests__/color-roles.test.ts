import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

// The site's color roles (#717, #764): ink (foreground/primary) marks the chosen thing and
// progress, and amber, the --link token, marks links and nothing else. #764 renamed the token from
// --accent, so a class still naming it (a branch from before the rename, say) styles nothing:
// Tailwind drops an unknown color without an error, and the link loses its color.

const SRC = path.resolve(__dirname, "../..");

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) return entry.name === "__tests__" ? [] : sourceFiles(full);
    return /\.(tsx?|css)$/.test(entry.name) ? [full] : [];
  });
}

// A Tailwind color utility (with any variants and opacity) that names the accent token. The CSS
// `accent-color` utility (`accent-primary`) names a color after "accent-", so it doesn't match.
const ACCENT_COLOR =
  /(?<![\w-])(?:[\w-]+:|\[[^\]]+\]:)*(?:text|bg|border(?:-[xytrbl])?|ring|outline|fill|stroke|decoration|divide|from|via|to|caret|shadow)-accent(?:-foreground)?(?:\/\d+)?(?![\w-])/g;

describe("color roles", () => {
  it("finds an accent class in a sample, and leaves the accent-color utility alone", () => {
    const sample = `"text-accent" "hover:text-accent" "[&_a]:text-accent" "bg-accent/10" "accent-primary" "text-link"`;
    expect(sample.match(ACCENT_COLOR)).toEqual(["text-accent", "hover:text-accent", "[&_a]:text-accent", "bg-accent/10"]);
  });

  it("no class names the old --accent color: links use text-link, everything else ink", () => {
    const found: string[] = [];
    for (const file of sourceFiles(SRC)) {
      const source = readFileSync(file, "utf8");
      for (const match of source.matchAll(ACCENT_COLOR)) {
        const line = source.slice(0, match.index).split("\n").length;
        found.push(`${path.relative(SRC, file)}:${line} ${match[0]}`);
      }
    }
    expect(found).toEqual([]);
  });

  it("defines --link in both palettes and no --accent", () => {
    const css = readFileSync(path.join(SRC, "app/globals.css"), "utf8");
    expect(css.match(/--link:\s*#[0-9a-f]{6};/g)).toHaveLength(2);
    expect(css).toContain("--color-link: var(--link);");
    expect(css).not.toMatch(/--(color-)?accent\b/);
  });
});
