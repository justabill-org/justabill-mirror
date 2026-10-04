import { afterEach, describe, it, expect, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import AboutPage, { metadata as aboutMetadata } from "@/app/(public)/(trust)/about/page";
import MethodologyPage, { metadata as methodologyMetadata } from "@/app/(public)/(trust)/methodology/page";
import PrivacyPage, { metadata as privacyMetadata } from "@/app/(public)/(trust)/privacy/page";
import TermsPage, { metadata as termsMetadata } from "@/app/(public)/(trust)/terms/page";
import ContactPage, { metadata as contactMetadata } from "@/app/(public)/(trust)/contact/page";
import {
  BASECAMP_POLICIES_URL,
  CC_BY_URL,
  CONTACT_EMAIL,
  POLICIES_LAST_UPDATED,
  POLICY_CHANGES,
  PRIVACY_EMAIL,
} from "@/lib/trust";

const pages = [
  { path: "/about", Page: AboutPage, metadata: aboutMetadata, title: "About | Just a Bill" },
  { path: "/methodology", Page: MethodologyPage, metadata: methodologyMetadata, title: "Methodology | Just a Bill" },
  { path: "/privacy", Page: PrivacyPage, metadata: privacyMetadata, title: "Privacy Policy | Just a Bill" },
  { path: "/terms", Page: TermsPage, metadata: termsMetadata, title: "Terms of Service | Just a Bill" },
  { path: "/contact", Page: ContactPage, metadata: contactMetadata, title: "Contact | Just a Bill" },
];

// Apostrophes and quotes come out as entities; compare against plain text.
function text(html: string): string {
  return html
    .replace(/<[^>]+>/g, " ")
    .replace(/&#x27;|&#39;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&amp;/g, "&")
    .replace(/\s+/g, " ");
}

describe("trust pages", () => {
  it.each(pages)("$path has its own title, description and canonical URL", ({ path, metadata, title }) => {
    expect(metadata.title).toBe(title);
    expect(typeof metadata.description).toBe("string");
    expect((metadata.description as string).length).toBeGreaterThan(50);
    expect(metadata.alternates?.canonical).toBe(path);
  });

  it("gives every page a different description", () => {
    expect(new Set(pages.map((p) => p.metadata.description)).size).toBe(pages.length);
  });

  it.each(pages)("$path renders one h1", ({ Page }) => {
    const html = renderToStaticMarkup(<Page />);
    expect(html.match(/<h1/g)).toHaveLength(1);
  });

  it("About states the mission and that the project is nonpartisan", () => {
    const body = text(renderToStaticMarkup(<AboutPage />));
    expect(body).toContain("Our mission");
    expect(body).toContain("isn't affiliated with any political party, candidate, campaign, advocacy group");
    expect(body).toContain("We don't choose which bills you see.");
    expect(body).toContain("Every summary is written the same way.");
  });

  it("About says who built the app and how AI agents built it under his decisions", () => {
    const html = renderToStaticMarkup(<AboutPage />);
    const body = text(html);
    expect(html).toContain('id="who-built-this"');
    expect(body).toContain("Just a Bill is one person's idea: someone who wanted");
    expect(body).toContain("He built it with Claude, Anthropic's AI model, working as a small team of coding agents.");
    expect(body).toContain("He approves every design before it's built");
    expect(body).not.toContain("Another AI model reviews every pull request");
    expect(body).toContain("The AI writes the code; he makes the decisions.");
    // The repo is private until the release mirror (#805): no promise of public designs or reviews (#806).
    expect(body).not.toMatch(/reviews are|public repository/);
  });

  it.each([
    { path: "/about", Page: AboutPage },
    { path: "/contact", Page: ContactPage },
    { path: "/methodology", Page: MethodologyPage },
    { path: "/terms", Page: TermsPage },
  ])("$path says, without a date, that we'll publish the code under Apache-2.0 (#806)", ({ Page }) => {
    expect(text(renderToStaticMarkup(<Page />))).toContain("We'll publish the code under the Apache-2.0 license.");
  });

  it("Methodology names each source, the summary model, the schedule and the scorecard rule", () => {
    const body = text(renderToStaticMarkup(<MethodologyPage />));
    for (const source of ["Congress.gov API", "GovInfo", "House Clerk", "Senate", "US Census Bureau geocoder"]) {
      expect(body).toContain(source);
    }
    expect(body).toContain("Gemini on Vertex AI");
    expect(body).toContain("not reviewed by a person");
    expect(body).toContain("every 6 hours");
    // sync-summaries runs every 30 minutes (summarySyncInterval in pipeline/cmd/serve/main.go, #588)
    expect(body).toContain("New AI summaries: every 30 minutes.");
    expect(body).toContain("final-passage-v1");
  });

  it("Methodology explains CRS summaries and why many bills have none (#423)", () => {
    const body = text(renderToStaticMarkup(<MethodologyPage />));
    expect(body).toContain("Congressional Research Service (CRS)");
    expect(body).toContain("CRS analysts write it, not AI");
    expect(body).toContain("many bills don't have one");
    expect(body).toContain("Official CRS summaries: every 6 hours.");
  });

  it("Methodology explains how law changes are found and explained, at the panel's anchor (#317)", () => {
    const html = renderToStaticMarkup(<MethodologyPage />);
    const body = text(html);
    // The bill page's "Changes to current law" panel links /methodology#law-changes.
    expect(html).toContain('id="law-changes"');
    expect(body).toContain("Office of the Law Revision Counsel");
    expect(body).toContain("release points");
    expect(body).toContain("MODS");
    expect(body).toContain("A fixed rule, not AI");
    expect(body).toContain("98.2% of the 2,671 sections");
    expect(body).toContain("in one request per bill");
    expect(body).toContain("Explanations aren't reviewed by a person and may contain errors.");
    expect(body).toContain("We don't show “the law as amended.”");
    expect(body).toContain("without the current text or an explanation");
    expect(body).toContain("The US Code: once a week.");
  });

  it("Methodology lists the Federal Register and explains CRA rule matching at the card's anchor (#643)", () => {
    const html = renderToStaticMarkup(<MethodologyPage />);
    const body = text(html);
    // The rule card on a CRA resolution's page links /methodology#cra-rules.
    expect(html).toContain('id="cra-rules"');
    expect(html).toContain('href="https://www.federalregister.gov/"');
    expect(body).toContain("Federal Register (the Office of the Federal Register at the National Archives");
    expect(body).toContain("we take the document published on that page");
    expect(body).toContain("exactly the same title, from a matching agency");
    expect(body).toContain("We show nothing rather than guess.");
    expect(body).toContain("FederalRegister.gov isn't the official edition");
    expect(body).toContain("The rules CRA resolutions disapprove, from the Federal Register: every 6 hours.");
  });

  it("Methodology quotes the summary prompt's CRA rule as prompt.go has it (#643)", async () => {
    const { CRA_PROMPT_RULE } = await import("@/lib/disapproved-rule");
    const html = renderToStaticMarkup(<MethodologyPage />);
    const quote = /<blockquote>([\s\S]*?)<\/blockquote>/.exec(html)?.[1] ?? "";
    expect(text(quote).replace(/&lt;/g, "<").replace(/&gt;/g, ">")).toBe(CRA_PROMPT_RULE);
  });

  it("Privacy covers browser votes, addresses, accounts, logs and the absence of trackers", () => {
    const body = text(renderToStaticMarkup(<PrivacyPage />));
    expect(body).toContain("Without an account, your votes stay in your browser.");
    expect(body).toContain("local storage");
    expect(body).toContain("We don't save the address in our database.");
    expect(body).toContain("Identity Platform");
    expect(body).toContain("never reach our database");
    expect(body).toContain("Vercel Web Analytics");
    expect(body).toContain("request logs");
    expect(body).toContain("requests each IP address can make per minute");
    expect(body).toContain("doesn't use advertising or tracking cookies");
  });

  describe("Privacy names the sign-in providers turned on in the build (#652)", () => {
    afterEach(() => vi.unstubAllEnvs());

    it("Google alone, and no other provider anywhere on the page", () => {
      vi.stubEnv("NEXT_PUBLIC_AUTH_PROVIDERS", "google.com");
      const body = text(renderToStaticMarkup(<PrivacyPage />));
      expect(body).toContain("You sign in with an existing Google account through Google Cloud Identity Platform.");
      expect(body).not.toMatch(/Apple|Microsoft/);
    });

    it("all three, in the listed order", () => {
      vi.stubEnv("NEXT_PUBLIC_AUTH_PROVIDERS", "google.com,microsoft.com,apple.com");
      const body = text(renderToStaticMarkup(<PrivacyPage />));
      expect(body).toContain("You sign in with an existing Google, Microsoft or Apple account through");
    });
  });

  it("Privacy describes the layout test cookie, its note and what's counted (#694)", async () => {
    const { COOKIE_NAME, MAX_DAYS } = await import("@/lib/experiments/registry");
    const { NOTE_KEY } = await import("@/lib/experiments/client");
    const html = renderToStaticMarkup(<PrivacyPage />);
    const body = text(html);
    expect(html).toContain('id="layout-tests"');
    expect(body).toContain("We use one cookie only while we test two layouts of a page");
    expect(body).toContain("and it doesn't identify you.");
    expect(body).toContain("expires when the test ends, within 30 days");
    expect(body).toContain("never how you voted");
    expect(body).toContain("Global Privacy Control signal, we always show the usual version, set no cookie and count nothing");
    expect(body).toContain("The layout test cookie: until the test ends, within 30 days.");
    // The page's numbers and names must match the code's.
    expect(MAX_DAYS).toBe(30);
    expect(COOKIE_NAME).toBe("jab_exp");
    expect(NOTE_KEY).toBe("jab.exp.v1");
  });

  it("Privacy names reCAPTCHA through App Check, what it collects and when it loads (#122)", () => {
    const html = renderToStaticMarkup(<PrivacyPage />);
    const body = text(html);
    expect(html).toContain('id="recaptcha"');
    expect(body).toContain("Firebase App Check");
    expect(body).toContain("only while you're signed in");
    expect(body).toContain("including your IP address");
    expect(body).toContain("If you don't sign in, no third-party scripts or fonts load");
    expect(body).not.toContain("No third-party scripts or fonts load in your browser.");
  });

  it("Privacy has a Sharing section saying what a share link holds and who sees it (#88, #165)", () => {
    const html = renderToStaticMarkup(<PrivacyPage />);
    const body = text(html);
    expect(html).toContain('id="sharing"');
    expect(html).toContain('href="#sharing"');
    expect(body).toContain("Nothing about a card is sent until you open the share dialog.");
    expect(body).toContain("doesn't include your other votes, your address, your account");
    expect(body).toContain("We don't save cards or links in our database.");
    expect(body).toContain("never the vote or counts on it");
    expect(body).toContain("Anyone with the link can see the card");
    expect(body).toContain("Share pages ask search engines not to list them.");
    // Aggregate cards (#166) name a place, which may be the sharer's own.
    expect(body).toContain("That card's web address holds only the bill and the place, and never your own vote.");
    expect(body).toContain("If the place you share is your own state or district, anyone with the link can see that place.");
    // A bill's own link (#810) holds only the bill, and its shares are counted.
    expect(body).toContain("You can also share a bill itself. Its link is the bill's page on our website");
    expect(body).toContain("Neither holds your vote, your address, your account or the page you shared it from.");
    expect(body).toContain("When you share a bill, we count how you shared it");
  });

  it("Privacy covers US state privacy rights and children, and the Terms set the same minimum age", () => {
    const html = renderToStaticMarkup(<PrivacyPage />);
    const body = text(html);
    expect(html).toContain('id="state-privacy-rights"');
    expect(html).toContain('id="children"');
    expect(body).toContain("We don't sell or share your personal information");
    expect(body).toContain("Global Privacy Control");
    expect(body).toContain("Your votes are sensitive.");
    expect(body).toContain("we'll answer within 45 days");
    expect(body).toContain("contact your state's attorney general");
    expect(body).toContain("you must be 13 or older to create an account");
    // The web server passes the visitor's IP to our API for its rate limit (#607).
    expect(body).toContain(
      "including the requests our website's server makes while showing you a page, for which it passes your IP " +
        "address to our API."
    );

    const terms = renderToStaticMarkup(<TermsPage />);
    expect(text(terms)).toContain("You must be 13 or older to create an account.");
    expect(terms).toContain('href="/privacy#children"');
  });

  it.each([
    { name: "Privacy", Page: PrivacyPage, lead: "This Privacy Policy is adapted" },
    { name: "Terms", Page: TermsPage, lead: "These Terms of Service are adapted" },
  ])("$name carries the date and the CC BY 4.0 attribution to Basecamp", ({ Page, lead }) => {
    const html = renderToStaticMarkup(<Page />);
    const body = text(html);
    expect(body).toContain(`Last updated: ${POLICIES_LAST_UPDATED}`);
    expect(body).toContain(`${lead} from the Basecamp (37signals) policies`);
    expect(body).toContain("We changed the text");
    expect(html).toContain(`href="${BASECAMP_POLICIES_URL}"`);
    expect(html).toContain(`href="${CC_BY_URL}"`);
  });

  it("Terms keeps Basecamp's warranty disclaimer and limitation of liability", () => {
    const body = text(renderToStaticMarkup(<TermsPage />));
    expect(body).toContain("on an “as is” and “as available” basis");
    expect(body).toContain("we shall not be liable, in law or in equity");
    expect(body).toContain("They may contain errors");
  });

  it("Terms names Kansas law and Kansas courts", () => {
    const html = renderToStaticMarkup(<TermsPage />);
    const body = text(html);
    expect(html).toContain('id="governing-law"');
    expect(body).toContain("The laws of the State of Kansas govern these Terms");
    expect(body).toContain("without regard to its conflict-of-law rules");
    expect(body).toContain("the state or federal courts located in Kansas");
  });

  it("Contact sends problems and security reports to the contact address, with Security in the subject (#806)", () => {
    const html = renderToStaticMarkup(<ContactPage />);
    const section = (id: string) => {
      const start = html.indexOf(`id="${id}"`);
      return html.slice(start, html.indexOf("</section>", start));
    };
    expect(section("report")).toContain('href="mailto:contact@justabill.io"');
    expect(section("security")).toContain('href="mailto:contact@justabill.io"');
    expect(text(section("security"))).toContain("with “Security” in the subject");
    expect(section("privacy")).toContain('href="mailto:privacy@justabill.io"');
  });

  it("Privacy and Terms date each significant change and list it, newest first (#806)", () => {
    // The date at the top is the newest entry's date, so the two can't drift apart.
    expect(POLICY_CHANGES[0].date).toBe(POLICIES_LAST_UPDATED);
    const times = POLICY_CHANGES.map(({ date }) => Date.parse(date));
    expect(times.every((t) => !Number.isNaN(t))).toBe(true);
    expect(times).toEqual([...times].sort((a, b) => b - a));

    const privacy = text(renderToStaticMarkup(<PrivacyPage />));
    expect(privacy).toContain("Each significant change is dated on this page");
    expect(privacy).not.toContain("public on GitHub");
    const terms = text(renderToStaticMarkup(<TermsPage />));
    expect(terms).toContain("list the change under Changes to these Terms");
    for (const { date, change } of POLICY_CHANGES) {
      expect(privacy).toContain(`${date}: ${change}`);
      expect(terms).toContain(`${date}: ${change}`);
    }
  });

  it("Contact and Privacy list the email addresses (#368)", () => {
    const contact = renderToStaticMarkup(<ContactPage />);
    expect(contact).toContain(`href="mailto:${CONTACT_EMAIL}"`);
    expect(contact).toContain(`href="mailto:${PRIVACY_EMAIL}"`);
    const privacy = renderToStaticMarkup(<PrivacyPage />);
    expect(privacy).toContain(`href="mailto:${PRIVACY_EMAIL}"`);
    expect(text(privacy)).toContain("Google Workspace");
  });
});
