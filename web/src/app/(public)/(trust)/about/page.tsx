import Link from "next/link";
import { Section, TrustPage } from "@/components/trust/trust-page";
import { PUBLIC_REPO_NAME, PUBLIC_REPO_URL, trustMetadata } from "@/lib/trust";

export const metadata = trustMetadata(
  "/about",
  "About",
  "Just a Bill is a nonpartisan app for reading bills in Congress in plain language, voting on them, " +
    "and comparing your votes with your representatives'.",
);

export default function AboutPage() {
  return (
    <TrustPage
      title="About Just a Bill"
      lead="Read what Congress is voting on, say how you would vote, and see how your representatives actually voted."
    >
      <Section id="mission" title="Our mission">
        <p>
          Bills are long, technical and hard to follow, and it&apos;s harder still to find out how your own members of
          Congress voted on them. Just a Bill puts both in one place: a plain-language summary of each bill next to its
          official text, a way to cast your own vote, and a scorecard that compares your votes with your
          representative&apos;s and your senators&apos;.
        </p>
        <p>
          We want anyone, whatever their politics, to be able to check what their representatives do, before they
          vote and between elections.
        </p>
      </Section>

      <Section id="nonpartisan" title="Nonpartisan by design">
        <ul>
          <li>
            Just a Bill isn&apos;t affiliated with any political party, candidate, campaign, advocacy group or
            government agency, and it doesn&apos;t endorse or oppose any of them.
          </li>
          <li>
            <strong>We don&apos;t choose which bills you see.</strong> Bills come from Congress.gov as Congress
            introduces them, and every bill goes through the same steps. Lists are sorted by plain facts such as when a
            bill was introduced or last acted on, never by topic or party.
          </li>
          <li>
            <strong>Every summary is written the same way.</strong> An AI model summarizes each bill from its official
            text using the same instructions for every bill, which ask for neutral language and no opinion on whether
            the bill is good or bad. Summaries aren&apos;t edited by hand for some bills and not others.{" "}
            <Link href="/methodology#summaries">How summaries are made</Link>.
          </li>
          <li>
            <strong>Vote records are the official ones.</strong> Members&apos; votes come from the House Clerk and the
            Senate&apos;s roll-call records, and the scorecard applies one published rule to every member.{" "}
            <Link href="/methodology#scorecard">How the scorecard works</Link>.
          </li>
          <li>
            <strong>No ads, and we don&apos;t sell data.</strong> Your votes are never used to target you with
            anything. See the <Link href="/privacy">Privacy Policy</Link>.
          </li>
        </ul>
      </Section>

      <Section id="code" title="The code">
        <p>
          The code of each release is published under the Apache-2.0 license at{" "}
          <a href={PUBLIC_REPO_URL}>{PUBLIC_REPO_NAME}</a>. We build in a private workspace and publish there with
          every release. That includes the rules that pick which vote counts and the instructions given to the AI
          model; the <Link href="/methodology">Methodology</Link> page explains how summaries and the scorecard are
          made, and links to both.
        </p>
      </Section>

      <Section id="who-built-this" title="Who built this">
        <p>
          Just a Bill is one person&apos;s idea: someone who wanted an app like this for a long time and decided
          in 2026 to get it built before the midterm elections.
        </p>
        <p>
          He built it with Claude, Anthropic&apos;s AI model, working as a small team of coding agents.
        </p>
        <p>
          <strong>The AI writes the code; he makes the decisions.</strong> He approves every design before it&apos;s
          built, and the rules above apply to everything the agents make.
        </p>
        <p>
          It came together quickly, which is one more reason to tell us when something looks wrong.
        </p>
      </Section>

      <Section id="mistakes" title="When we get something wrong">
        <p>
          Summaries can contain errors and data can lag behind Congress. Always check the official text before relying
          on a summary, and <Link href="/contact">tell us</Link> when something looks wrong.
        </p>
      </Section>
    </TrustPage>
  );
}
