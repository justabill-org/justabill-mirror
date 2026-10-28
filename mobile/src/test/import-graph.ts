// Walks the import graph of the web modules the app shares (src/shared.ts), for
// src/__tests__/shared-imports.test.ts. A test utility, not app code.
import fs from "node:fs";
import path from "node:path";
import ts from "typescript";

export interface GraphOptions {
  /** The web's src directory, where @/ names point. */
  webSrc: string;
  /** The app's own API client, what @/lib/api names in the app. Not walked into. */
  apiClient: string;
  /** Packages a shared module may import. */
  allow: readonly string[];
}

export interface Violation {
  /** The importing file, relative to the web's src directory or the entry's directory. */
  file: string;
  specifier: string;
  reason: string;
}

export interface Graph {
  /** The shared modules reached, as @/ names without an extension (e.g. "@/lib/vote-deck"). */
  modules: string[];
  violations: Violation[];
}

const EXTENSIONS = [".ts", ".tsx", "/index.ts", "/index.tsx"];

function resolveFile(base: string): string | undefined {
  if (/\.tsx?$/.test(base) && fs.existsSync(base)) return base;
  return EXTENSIONS.map((ext) => base + ext).find((f) => fs.existsSync(f));
}

/** Every specifier a file imports or re-exports, type-only imports included: tsc reads those too. */
function importsOf(file: string): string[] {
  const info = ts.preProcessFile(fs.readFileSync(file, "utf8"), true, true);
  return info.importedFiles.map((f) => f.fileName);
}

/**
 * Walks every import reachable from `entry`. Each one must be @/lib/api (the app's client), a file
 * under the web's src/lib (walked in turn), or a package in `allow`; anything else is a violation.
 */
export function walkImports(entry: string, opts: GraphOptions): Graph {
  const lib = path.join(opts.webSrc, "lib") + path.sep;
  const seen = new Set<string>();
  const violations: Violation[] = [];
  const label = (f: string) =>
    f.startsWith(opts.webSrc) ? path.relative(opts.webSrc, f) : path.relative(path.dirname(entry), f);

  const visit = (file: string) => {
    if (seen.has(file)) return;
    seen.add(file);
    for (const specifier of importsOf(file)) {
      const fail = (reason: string) => violations.push({ file: label(file), specifier, reason });
      if (specifier === "@/lib/api") continue;
      let base: string;
      if (specifier.startsWith("@/")) base = path.join(opts.webSrc, specifier.slice(2));
      else if (specifier.startsWith(".")) base = path.resolve(path.dirname(file), specifier);
      else {
        if (!opts.allow.includes(specifier)) fail("a package outside the allowlist");
        continue;
      }
      const target = resolveFile(base);
      if (!target) fail("doesn't resolve to a file");
      else if (target === opts.apiClient) continue;
      else if (!target.startsWith(lib)) fail("outside web/src/lib");
      else visit(target);
    }
  };
  visit(entry);

  const modules = [...seen]
    .filter((f) => f.startsWith(lib))
    .map((f) => "@/" + path.relative(opts.webSrc, f).replace(/\.tsx?$/, "").split(path.sep).join("/"))
    .sort();
  return { modules, violations };
}
