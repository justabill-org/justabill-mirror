// The shared modules stay platform-neutral (docs/design/1091-phone-apps.md): everything reachable
// from src/shared.ts is another web/src/lib module, @/lib/api (the app's own client) or an allowed
// package. A web change that adds React, Next.js, Firebase or the server key to one fails here.
import path from "node:path";
import { walkImports } from "../test/import-graph";

const root = path.resolve(__dirname, "../..");
const opts = {
  webSrc: path.resolve(root, "../web/src"),
  apiClient: path.join(root, "src/lib/api.ts"),
  allow: ["clsx", "tailwind-merge"],
};
const fixture = (name: string) => path.join(root, "src/test/fixtures", name);

describe("the shared modules' imports", () => {
  const graph = walkImports(path.join(root, "src/shared.ts"), opts);

  it("stay inside web/src/lib, the app's API client and the allowlist", () => {
    expect(graph.violations).toEqual([]);
  });

  it("include every module the app re-exports", () => {
    expect(graph.modules).toEqual(
      expect.arrayContaining([
        "@/lib/types",
        "@/lib/vote-deck",
        "@/lib/my-votes",
        "@/lib/scorecard",
        "@/lib/local/votes",
        "@/lib/local/reps",
        "@/lib/local/storage",
        "@/lib/votes/import",
        "@/lib/votes/account",
        "@/lib/votes/backend",
        "@/lib/auth/app-check",
        "@/lib/auth/onboarding",
        "@/lib/bill-filters",
        "@/lib/bill-views",
        "@/lib/bill-progress",
        "@/lib/paging",
        "@/lib/districts",
        "@/lib/congress",
        "@/lib/text-versions",
      ])
    );
  });

  it("never reach the web's API client, which imports the server key", () => {
    expect(graph.modules).not.toContain("@/lib/api");
  });
});

describe("walkImports", () => {
  it("fails a module that imports react", () => {
    const { violations, modules } = walkImports(fixture("imports-react.ts"), opts);
    expect(violations).toEqual([
      { file: "imports-react.ts", specifier: "react", reason: "a package outside the allowlist" },
    ]);
    // It still walks the shared module beside it, and what that imports.
    expect(modules).toEqual(["@/lib/bill-filters", "@/lib/bill-views", "@/lib/congress", "@/lib/graph", "@/lib/paging", "@/lib/types"]);
  });

  it("fails web code outside src/lib, the server key and a missing module, and allows @/lib/api", () => {
    const { violations, modules } = walkImports(fixture("imports-outside-lib.ts"), opts);
    expect(violations).toEqual([
      { file: "imports-outside-lib.ts", specifier: "@/components/layout/skip-link", reason: "outside web/src/lib" },
      { file: "imports-outside-lib.ts", specifier: "#server-key", reason: "a package outside the allowlist" },
      { file: "imports-outside-lib.ts", specifier: "@/lib/no-such-module", reason: "doesn't resolve to a file" },
    ]);
    expect(modules).toEqual([]);
  });

  it("fails a type-only import of a package outside the allowlist", () => {
    const { violations } = walkImports(path.join(opts.webSrc, "lib/bill-metadata.ts"), opts);
    expect(violations).toContainEqual({ file: "lib/bill-metadata.ts", specifier: "next", reason: "a package outside the allowlist" });
  });
});
