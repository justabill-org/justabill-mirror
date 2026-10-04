import Link from "next/link";
import { Section, TrustPage } from "@/components/trust/trust-page";
import { CODE_PUBLICATION, trustMetadata } from "@/lib/trust";
import { CRA_EFFECT_URL, CRA_PROMPT_RULE } from "@/lib/disapproved-rule";

export const metadata = trustMetadata(
  "/methodology",
  "Methodology",
  "Where Just a Bill's bills, votes and district data come from, how the AI summaries are made, how we find and " +
    "explain what a bill changes in current law, how often data updates, how the scorecard counts votes, and how " +
    "we publish how Just a Bill users voted.",
);

// Sources: pipeline/internal/congress, govinfo, legislators and sync/votes.go; the summary prompt in
// pipeline/internal/ai/prompt.go; the schedule in pipeline/cmd/serve (sync intervals); the district
// lookup in api/internal/district; the scorecard rule in db/scoring (#69); the law changes in
// pipeline/internal/uscode, pipeline/internal/billtext/lawrefs.go and pipeline/internal/ai/law.go (#149), with
// the classifier's agreement rate from billtext's TestUSLMCrossCheck (PR #430); the user-vote aggregates' rules
// in pipeline/internal/aggregates (config.go defaults, publish.go, rollup.go) and design #89; the rules CRA
// resolutions disapprove in pipeline/internal/cra and pipeline/internal/sync/crarules.go (design #590), and the
// prompt rule quoted under #cra-rules in systemInstructionBill.
// The publication rules for user-vote aggregates, from "The rules, in one place" in design #89 and the
// defaults in pipeline/internal/aggregates/config.go.
const AGGREGATE_RULES: readonly [string, string][] = [
  ["Who counts", "Signed-in accounts only, at least 48 hours old."],
  ["Bot check", "A vote counts only if it passed our automated check that it came from our app when it was cast."],
  ["Changing district", "An account can change its district at most once every 30 days."],
  ["Votes a day", "An account can cast at most 500 votes a day."],
  ["Skips", "Skipped bills aren't counted. Percentages are of Yea and Nay votes."],
  ["Minimum to show", "50 counted votes for a state or district, 100 nationwide. Below that: “Not enough votes yet.”"],
  ["Rounding", "Percentages to whole numbers; the number of users rounded down to the nearest 10 (“340+ users”)."],
  ["Updates", "Checked every hour; a place's numbers change only after at least 10 votes have changed."],
];

export default function MethodologyPage() {
  return (
    <TrustPage
      title="Methodology"
      lead="Where our data comes from, how summaries and explanations of law changes are written, and how the scorecard counts votes."
    >
      <Section id="sources" title="Data sources">
        <p>Everything on Just a Bill comes from public, official sources:</p>
        <ul>
          <li>
            <a href="https://api.congress.gov/" target="_blank" rel="noopener noreferrer">
              Congress.gov API
            </a>{" "}
            (Library of Congress): bills, their actions, sponsors and cosponsors, committees, subjects, text versions,
            amendments, and current members of Congress, with the official member photos shown on scorecard cards and member pages.
          </li>
          <li>
            <a href="https://www.govinfo.gov/" target="_blank" rel="noopener noreferrer">
              GovInfo
            </a>{" "}
            (US Government Publishing Office): the official text of each version of a bill, and related GAO reports.
          </li>
          <li>
            <a href="https://clerk.house.gov/Votes" target="_blank" rel="noopener noreferrer">
              House Clerk
            </a>{" "}
            and{" "}
            <a href="https://www.senate.gov/legislative/votes.htm" target="_blank" rel="noopener noreferrer">
              Senate
            </a>{" "}
            roll-call vote files: how each member voted on each recorded vote.
          </li>
          <li>
            <a href="https://github.com/unitedstates/congress-legislators" target="_blank" rel="noopener noreferrer">
              congress-legislators
            </a>{" "}
            (a public-domain dataset maintained by volunteers): the terms of former members, so past votes are shown
            with the right name and state.
          </li>
          <li>
            <a href="https://geocoding.geo.census.gov/" target="_blank" rel="noopener noreferrer">
              US Census Bureau geocoder
            </a>
            : which congressional district an address is in. We use the Census Bureau&apos;s current congressional district map.
          </li>
          <li>
            <a href="https://uscode.house.gov/" target="_blank" rel="noopener noreferrer">
              Office of the Law Revision Counsel
            </a>{" "}
            (US House of Representatives): the United States Code, the federal laws currently in force, arranged by
            subject.
          </li>
          <li>
            <a href="https://www.federalregister.gov/" target="_blank" rel="noopener noreferrer">
              Federal Register
            </a>{" "}
            (the Office of the Federal Register at the National Archives, and the US Government Publishing Office): the
            agency rules that Congressional Review Act resolutions disapprove (<a href="#cra-rules">how we match
            them</a>).
          </li>
        </ul>
        <p>
          We don&apos;t edit these records. If a source has a mistake, we show it until the source corrects it; if you
          spot one, <Link href="/contact">tell us</Link>.
        </p>
      </Section>

      <Section id="summaries" title="How summaries are made">
        <ul>
          <li>
            Each bill&apos;s plain-language summary is written by an AI model, Google&apos;s Gemini on Vertex AI, from the
            bill&apos;s official text. When a bill changes between versions, the model also
            summarizes what changed.
          </li>
          <li>
            Every bill gets the same instructions: describe only what the text would do, in neutral words; don&apos;t
            predict effects, costs, winners or losers; don&apos;t call anything good or bad; and don&apos;t mention
            parties or politicians unless the bill names them. Very long bills may be cut short, and the summary then
            says it covers only part of the bill.
          </li>
          <li>
            <strong>
              Summaries are not reviewed by a person before they&apos;re published, and they may contain errors.
            </strong>{" "}
            Each one shows the date it was generated and links to the official text on Congress.gov. The official text is
            always the authority.
          </li>
          <li>
            New bills can take a while to get a summary: we summarize a limited number of bills each run, and a bill
            whose text isn&apos;t published yet can&apos;t be summarized.
          </li>
          <li>
            Many bills also show an <strong>official summary</strong> by the Congressional Research Service (CRS), the
            nonpartisan research agency that serves Congress. CRS analysts write it, not AI, and we show it as
            Congress.gov publishes it, with the version of the bill it describes and a link to it there. CRS writes a
            summary when a bill is introduced and again as it changes, often months later, so many bills don&apos;t have
            one: in September 2026, about 3 in 10 bills of the 119th Congress did. When a bill&apos;s text has changed
            since its official summary, the summary says so.
          </li>
          <li>
            When a bill has an official CRS summary, the AI model is given it too, as context to help it understand
            long or technical bills. It&apos;s told to describe what the bill&apos;s text does, to follow the text
            where the two differ, and to write in its own words rather than copy the CRS summary. The AI summary then
            says it was written with the CRS summary as context, and a new CRS summary gets the bill a fresh AI
            summary.
          </li>
        </ul>
      </Section>

      <Section id="law-changes" title="Changes to current law">
        <p>
          Many bills work by changing laws already on the books. On a bill&apos;s page, &ldquo;Changes to current
          law&rdquo; lists the sections of the United States Code that the bill&apos;s latest text would amend, repeal
          or add, and explains each change.
        </p>
        <ul>
          <li>
            <strong>Current law</strong> is the US Code as the Office of the Law Revision Counsel publishes it, in
            &ldquo;release points&rdquo; that are each current through a given public law. We load each new release
            point within a week, and the panel says which public law its text is current through.
          </li>
          <li>
            <strong>What a bill changes</strong> is found in the bill&apos;s own text. Its official XML from GovInfo
            marks many of the US Code sections it cites, and GovInfo&apos;s catalog record for each version (its MODS
            metadata) lists more. A fixed rule, not AI, reads the sentence around each citation: &ldquo;is
            amended&rdquo; means the bill amends the section, &ldquo;is repealed&rdquo; that it repeals it, and
            &ldquo;the following new section&rdquo; that it adds one. Anything else only mentions the section, and
            isn&apos;t listed. For enacted bills, GovInfo also publishes a version of the text that marks every
            amendment; checked against it on 173 enacted bills of the 118th and 119th Congresses in September 2026,
            the rule agreed on 98.2% of the 2,671 sections both found about whether the bill changes the section or only mentions it.
          </li>
          <li>
            <strong>Explanations</strong> are written by an AI model, Google&apos;s Gemini on Vertex AI, in one request
            per bill. It gets the bill&apos;s instruction for each section and the section&apos;s current text, and
            follows the same instructions for every bill: say what the provision says now and what it would say or do
            after the change, in neutral words, without predicting effects or saying whether the change is good or bad.
            The panel labels the explanations as AI and
            names the model and the date they were written. <strong>Explanations aren&apos;t reviewed by a person and may contain errors.</strong>{" "}
            Next to each one, the panel shows the bill&apos;s instruction and lets you open the section&apos;s current text,
            so you can check it.
          </li>
          <li>
            <strong>We don&apos;t show &ldquo;the law as amended.&rdquo;</strong> We never rewrite a section with the
            bill&apos;s changes applied: the explanation describes the change, and the official text of the bill and of
            the US Code is always the authority.
          </li>
          <li>
            <strong>What isn&apos;t explained.</strong> When a bill changes a law by its name, such as &ldquo;section 4
            of the Social Security Act,&rdquo; without citing the US Code, we list the change as &ldquo;Not in the US
            Code&rdquo; with the bill&apos;s instruction, but without the current text or an explanation. A statutory
            note, such as &ldquo;10 U.S.C. 4271 note,&rdquo; is law the US Code prints after a section without making
            it part of the section&apos;s text. We list and explain a change to one, but we don&apos;t load notes, so
            the explanation comes from the bill&apos;s instruction alone and there&apos;s no current text to open.
            When a bill changes more sections than one request can cover, the rest are listed without an
            explanation. New explanations are written every hour, up to a daily limit, so a new bill may list its
            changes before they&apos;re explained. A section is explained again when the bill&apos;s text or the
            section&apos;s current text changes.
          </li>
        </ul>
      </Section>

      <Section id="cra-rules" title="Rules disapproved under the Congressional Review Act">
        <p>
          Under the Congressional Review Act, Congress can pass a joint resolution disapproving a rule a federal
          agency has issued. If the resolution becomes law, the rule has no force or effect, and the agency
          can&apos;t issue a substantially similar rule unless a later law authorizes it (
          <a href={CRA_EFFECT_URL} target="_blank" rel="noopener noreferrer">
            5 U.S.C. 801
          </a>
          ). The page of such a resolution shows the rule it disapproves, as the Federal Register published it.
        </p>
        <ul>
          <li>
            <strong>How we match a resolution to its rule.</strong> A fixed rule, not AI, reads the rule&apos;s
            title, its agency and any Federal Register citation from the resolution&apos;s text. When the text cites
            a page, we take the document published on that page, as long as its title shares at least half of the
            main words of the title the resolution names. Otherwise, we look for a rule or notice with exactly the
            same title, from a matching agency, published before the resolution was introduced. When more than one
            fits, we take the newest only if it was published within two years before the resolution and at least
            a year after the next one; otherwise we don&apos;t pick one. When the text cites a proposed rule and
            the final rule has the same title, we show the final rule and say what the text cites. A rule matched
            by its title, not its citation, says so on the page.
          </li>
          <li>
            <strong>We show nothing rather than guess.</strong> When we can&apos;t match a resolution to one
            document, its page says so, shows no rule, and links a Federal Register search for the title it names.
            Some resolutions identify an agency action by its date and a Government Accountability Office opinion
            that it is a rule, rather than by a Federal Register citation, and these often can&apos;t be matched.
            We check unmatched resolutions again every 30 days, and any resolution again when its text changes.
          </li>
          <li>
            <strong>What we show</strong> is the document&apos;s title, agencies, dates, citation and the abstract
            the agency wrote, as published, with links to the document on FederalRegister.gov, its docket on
            Regulations.gov, and the official PDF on GovInfo. FederalRegister.gov isn&apos;t the official edition of
            the Federal Register; the PDF is. When the disapproved document withdrew an earlier one, we show that
            one too. None of it is written by AI.
          </li>
          <li>
            <strong>AI summaries of these resolutions</strong> are written with the rule&apos;s title, agency,
            dates and abstract as context, and say so above the summary. A resolution gets a fresh summary when
            that context changes. The model is given this rule along with the others:
            <blockquote>{CRA_PROMPT_RULE}</blockquote>
          </li>
        </ul>
      </Section>

      <Section id="updates" title="How often data updates">
        <p>Our data pipeline checks the sources on a fixed schedule:</p>
        <ul>
          <li>Changes to bills on GovInfo: every 30 minutes.</li>
          <li>New bill text: every 2 hours.</li>
          <li>Bills and their actions: every 4 hours.</li>
          <li>Recorded votes: every 6 hours.</li>
          <li>Members of Congress: once a day.</li>
          <li>New AI summaries: every 30 minutes.</li>
          <li>Official CRS summaries: every 6 hours.</li>
          <li>New AI explanations of changes to law: every hour.</li>
          <li>The US Code: once a week.</li>
          <li>The rules CRA resolutions disapprove, from the Federal Register: every 6 hours.</li>
        </ul>
        <p>
          So something that happens in Congress usually shows up here within hours, and a new vote within about a day,
          depending on when the source publishes it.
        </p>
      </Section>

      <Section id="scorecard" title="How the scorecard works">
        <p>
          When you vote on a bill, we compare your vote with how each of your representatives voted on it. We use only
          the most recent final vote each chamber recorded on the bill itself, such as passage, agreeing to a
          resolution, or overriding a veto, never procedural votes or votes on amendments. Bills passed by voice vote
          or unanimous consent have no record of individual votes, so they can&apos;t be compared, and &ldquo;Not
          voting&rdquo; or &ldquo;Present&rdquo; doesn&apos;t count for or against anyone.
        </p>
        <p>
          The rule is named <code>final-passage-v1</code>, and it&apos;s the same for every member.{" "}
          {CODE_PUBLICATION} That includes the code that applies this rule.
        </p>
      </Section>

      <Section id="aggregates" title="How Just a Bill users voted">
        <p>
          Bill pages can show how Just a Bill users voted on a bill, nationwide, by state and by congressional district,
          next to how that district&apos;s representative and the state&apos;s senators voted. The scorecard can add how
          often users in your district or state agreed with each of your representatives.
        </p>
        <p>
          <strong>These numbers are not a poll.</strong> They count opt-in Just a Bill users who chose to vote and who
          say they live in a place, not a sample of its residents, so they can&apos;t tell you what a district or a
          state thinks. We publish them with these rules:
        </p>
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <caption className="sr-only">The rules for publishing how Just a Bill users voted</caption>
            <thead>
              <tr className="border-b border-border">
                <th scope="col" className="py-2 pr-4 font-semibold">Rule</th>
                <th scope="col" className="py-2 font-semibold">What we do</th>
              </tr>
            </thead>
            <tbody>
              {AGGREGATE_RULES.map(([rule, what]) => (
                <tr key={rule} className="border-b border-border align-top">
                  <th scope="row" className="py-2 pr-4 font-medium">{rule}</th>
                  <td className="py-2">{what}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <ul>
          <li>
            <strong>Under review.</strong> If votes in a place arrive in a sudden burst, mostly from new accounts, or
            swing sharply, we keep showing the last published numbers marked &ldquo;Under review&rdquo; until a person
            has looked. Real interest and coordinated voting can look the same, so a person decides.
          </li>
          <li>
            <strong>Representatives&apos; votes</strong> follow the scorecard&apos;s rule above: the most recent final
            vote on the bill, with &ldquo;Not voting&rdquo; and &ldquo;Present&rdquo; shown but not counted. A
            representative &ldquo;agreed&rdquo; with users when they voted the way most of the place&apos;s published
            users did; bills where users were split evenly aren&apos;t counted.
          </li>
          <li>
            Votes kept only in your browser, without an account, are never counted or sent to us.
          </li>
        </ul>
        <p>
          The numbers above are our code&apos;s defaults; we may make them stricter if we see abuse.
        </p>
      </Section>

      <Section id="corrections" title="Corrections">
        <p>
          If a summary or an explanation gets a bill wrong, a vote is classified wrongly, or your district looks off,{" "}
          <Link href="/contact">report it</Link>.
        </p>
      </Section>
    </TrustPage>
  );
}
