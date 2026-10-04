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
import { PUBLIC_REPO_URL } from "@/lib/trust";

// #806, #822: we build in a private workspace and publish each release's code to the public repo (#805), so
// a page may link to that repo, never to the workspace or any other repo of ours. The other github.com links
// allowed are other people's public repositories we must credit. No page may say the code is open source, or
// promise a public trail of designs or reviews: only released code is public.
const ALLOWED_GITHUB = [
  PUBLIC_REPO_URL, // the public repo, and any file in it
  "https://github.com/basecamp/policies", // /privacy and /terms adapt it under CC BY 4.0, which needs the credit
  "https://github.com/unitedstates/congress-legislators", // a data source named on /methodology
];

const OPEN_SOURCE = /open[\s-]?source/i;
const PUBLIC_TRAIL =
  /\b(?:(?:built|developed|designed|working)\s+in\s+(?:the\s+)?(?:open|public)|open\s+development|public\s+(?:designs?|design\s+docs?|reviews?|roadmap|discussions?))\b/i;
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
    ["The code of each release is published under the Apache-2.0 license.", false],
  ])("OPEN_SOURCE matching %j is %s", (input, want) => {
    expect(OPEN_SOURCE.test(input)).toBe(want);
  });

  it.each([
    ["Just a Bill is built in the open.", true],
    ["developed in public", true],
    ["Read our public designs and reviews.", true],
    ["Follow open development on GitHub.", true],
    ["the public design docs", true],
    ["We build in a private workspace and publish there with every release.", false],
    ["Votes made in public view", false],
  ])("PUBLIC_TRAIL matching %j is %s", (input, want) => {
    expect(PUBLIC_TRAIL.test(input)).toBe(want);
  });

  it.each([
    [
      '<a href="https://github.com/justabill-org/justabill/issues/new">',
      ["https://github.com/justabill-org/justabill"],
    ],
    ['<a href="https://docs.github.com/site-policy">', ["github.com/site-policy"]],
    ["see github.com/someone", ["github.com/someone"]],
    ['<a href="https://github.com/justabill-org/justabill">', ["https://github.com/justabill-org/justabill"]],
    ['<a href="https://github.com/justabill-org/justabill-mirror-old">', ["https://github.com/justabill-org/justabill-mirror-old"]],
    ['<a href="https://github.com/justabill-org/justabill-mirror">', []],
    ['<a href="https://github.com/justabill-org/justabill-mirror/blob/main/CONTRIBUTING.md">', []],
    ['<a href="https://github.com/basecamp/policies">', []],
    ["(github.com/basecamp/policies, CC BY 4.0)", []],
  ])("githubLinks(%j) is %j", (input, want) => {
    expect(githubLinks(input)).toEqual(want);
  });
});

describe("public pages link only to the public repo, and promise no public trail (#806, #822)", () => {
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
    expect(html).not.toMatch(PUBLIC_TRAIL);
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
        const found = [
          ...(OPEN_SOURCE.exec(source) ?? []),
          ...(PUBLIC_TRAIL.exec(source) ?? []),
          ...githubLinks(source),
        ];
        return found.map((what) => `${file}: ${what}`);
      });
    expect(offenders).toEqual([]);
  });
});
