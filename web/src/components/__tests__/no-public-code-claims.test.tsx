import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import AboutPage from "@/app/(public)/(trust)/about/page";
import ContactPage from "@/app/(public)/(trust)/contact/page";
import MethodologyPage from "@/app/(public)/(trust)/methodology/page";
import PrivacyPage from "@/app/(public)/(trust)/privacy/page";
import TermsPage from "@/app/(public)/(trust)/terms/page";
import { Footer } from "@/components/layout/footer";

// #806: until the release mirror exists (#805), the repo is private, so no public page may say the code is
// open source or send anyone to GitHub. The only github.com links allowed are other people's public
// repositories we must credit.
const ALLOWED_GITHUB = [
  "https://github.com/basecamp/policies", // /privacy and /terms adapt it under CC BY 4.0, which needs the credit
  "https://github.com/unitedstates/congress-legislators", // a data source named on /methodology
];

const OPEN_SOURCE = /open[\s-]?source/i;
const GITHUB_URL = /(?:https?:\/\/)?(?:www\.)?github\.com(?:\/[\w.-]+){0,2}/gi;

/** github.com URLs in the text, other than the allowed ones. */
function githubLinks(source: string): string[] {
  return [...source.matchAll(GITHUB_URL)]
    .map((m) => m[0])
    .filter((url) => !ALLOWED_GITHUB.some((ok) => url.replace(/^(?:https?:\/\/)?(?:www\.)?/i, "https://") === ok));
}

describe("guards", () => {
  it.each([
    ["the code is open source", true],
    ["an Open-Source project", true],
    ["opensource", true],
    ["We'll publish the code under the Apache-2.0 license.", false],
  ])("OPEN_SOURCE matching %j is %s", (input, want) => {
    expect(OPEN_SOURCE.test(input)).toBe(want);
  });

  it.each([
    [
      '<a href="https://github.com/justabill-org/justabill/issues/new">',
      ["https://github.com/justabill-org/justabill"],
    ],
    ['<a href="https://docs.github.com/site-policy">', ["github.com/site-policy"]],
    ["see github.com/someone", ["github.com/someone"]],
    ['<a href="https://github.com/basecamp/policies">', []],
    ["(github.com/basecamp/policies, CC BY 4.0)", []],
  ])("githubLinks(%j) is %j", (input, want) => {
    expect(githubLinks(input)).toEqual(want);
  });
});

describe("public pages never say the code is public or link to GitHub (#806)", () => {
  it.each([
    ["/about", AboutPage],
    ["/contact", ContactPage],
    ["/methodology", MethodologyPage],
    ["/privacy", PrivacyPage],
    ["/terms", TermsPage],
    ["the footer", Footer],
  ])("%s", (_name, Page) => {
    const html = renderToStaticMarkup(<Page />);
    expect(html).not.toMatch(OPEN_SOURCE);
    expect(githubLinks(html)).toEqual([]);
  });

  // Pages that need the API (the home page, bills, members) can't all render here, so read every file
  // that can put text on a page: the app routes, the components and the helpers they use, and public/.
  const web = path.resolve(__dirname, "../../..");
  const shipped = (dir: string): string[] =>
    readdirSync(path.join(web, dir), { recursive: true, withFileTypes: true })
      .filter((e) => e.isFile() && !e.parentPath.includes("__tests__") && !/\.test\.tsx?$/.test(e.name))
      .map((e) => path.relative(web, path.join(e.parentPath, e.name)));
  const files = ["src/app", "src/components", "src/lib", "public"].flatMap(shipped);

  it("finds the files to check", () => {
    expect(files).toContain("src/app/page.tsx");
    expect(files).toContain("src/components/layout/footer.tsx");
  });

  it("in any file that can put text on a page", () => {
    const offenders = files
      .filter((f) => /\.(tsx?|md|txt|json|html|xml)$/.test(f))
      .flatMap((file) => {
        const source = readFileSync(path.join(web, file), "utf8");
        const found = [...(OPEN_SOURCE.exec(source) ?? []), ...githubLinks(source)];
        return found.map((what) => `${file}: ${what}`);
      });
    expect(offenders).toEqual([]);
  });
});
