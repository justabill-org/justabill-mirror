import Link from "next/link";
import { PolicyAttribution, PolicyChanges, Section, TrustPage } from "@/components/trust/trust-page";
import { POLICIES_LAST_UPDATED, PUBLIC_REPO_NAME, PUBLIC_REPO_URL, trustMetadata } from "@/lib/trust";

export const metadata = trustMetadata(
  "/terms",
  "Terms of Service",
  "The terms you agree to when you use Just a Bill, including that AI summaries may contain errors and the limits " +
    "of our liability.",
);

// Adapted from Basecamp's Terms of Service and Use Restrictions policy (github.com/basecamp/policies,
// CC BY 4.0, commit d39f6f1). The warranty disclaimer and limitation of liability keep Basecamp's
// wording; what covers payments, their products and their subprocessors is gone. The governing-law section is not
// Basecamp's: they name Kansas and no county, so the venue is any court located in Kansas.
export default function TermsPage() {
  return (
    <TrustPage
      title="Terms of Service"
      lead="The rules for using Just a Bill, written to be read."
      lastUpdated={POLICIES_LAST_UPDATED}
    >
      <p>
        Thank you for using Just a Bill! Because we don&apos;t know everyone who uses it personally, we have to put in
        place some Terms of Service to help keep the ship afloat.
      </p>
      <p>
        When we say &ldquo;we&rdquo;, &ldquo;our&rdquo; or &ldquo;us&rdquo;, we mean the people who run Just a Bill.
        When we say the &ldquo;Service&rdquo;, we mean the Just a Bill website, its API and anything else we build
        under the Just a Bill name. When we say &ldquo;you&rdquo; or &ldquo;your&rdquo;, we mean anyone who uses the
        Service, with or without an account.
      </p>
      <p>
        We may update these Terms in the future. Whenever we make a significant change, we will refresh the date at the
        top of this page and list the change under <a href="#changes">Changes to these Terms</a>.
      </p>
      <p>
        When you use the Service, now or in the future, you are agreeing to the latest Terms. There may be times where
        we do not exercise or enforce a right or provision of the Terms; however, that does not mean we are waiving that
        right or provision. <strong>These Terms do contain a limitation of our liability.</strong>
      </p>

      <Section id="information" title="Information, not advice">
        <ol>
          <li>
            Bill summaries on Just a Bill are written by an AI model and are not reviewed by a person before they are
            published. <strong>They may contain errors or leave out important details.</strong> The official text of a
            bill, linked from every summary, is the only authoritative version.
          </li>
          <li>
            Bills, actions and vote records come from official sources such as Congress.gov, GovInfo, the House Clerk
            and the Senate. We copy them on a schedule, so they can be out of date or incomplete, and a source can have
            its own errors. <Link href="/methodology">Our methodology</Link> explains where each piece comes from.
          </li>
          <li>
            The scorecard compares your votes with your representatives&apos; using the published rule described in
            our methodology. It is one way of measuring agreement, not a rating of anyone&apos;s performance or
            character.
          </li>
          <li>
            Nothing on Just a Bill is legal, financial or voting advice, and it isn&apos;t an endorsement of any bill,
            party or candidate. Just a Bill isn&apos;t part of or affiliated with Congress or any government agency.
            Voting on a bill here doesn&apos;t send your view to Congress or to your representatives.
          </li>
        </ol>
      </Section>

      <Section id="accounts" title="Account terms">
        <p>You can read bills, vote on them and compare your votes without an account. If you do create one:</p>
        <ol>
          <li>
            You must be 13 or older to create an account. We don&apos;t knowingly let children under 13 sign up, and
            we delete any account we learn belongs to one (see the{" "}
            <Link href="/privacy#children">Privacy Policy</Link>).
          </li>
          <li>
            You are responsible for maintaining the security of your account and the account you sign in with. We
            cannot and will not be liable for any loss or damage from your failure to comply with this security
            obligation.
          </li>
          <li>
            You may not use the Service for any purpose outlined in the <a href="#use-restrictions">use restrictions</a>{" "}
            below.
          </li>
          <li>You are responsible for all activity that occurs under your account.</li>
          <li>You must be a human. Accounts registered by &ldquo;bots&rdquo; or other automated methods are not permitted.</li>
        </ol>
      </Section>

      <Section id="free" title="It's free">
        <p>
          Just a Bill is free: we don&apos;t ask you for a credit card, we don&apos;t show ads, and we do not sell your
          data.
        </p>
      </Section>

      <Section id="termination" title="Cancellation and termination">
        <ol>
          <li>
            You can stop using the Service at any time. The <Link href="/privacy#your-rights">Privacy Policy</Link>{" "}
            explains how to delete what we hold about you.
          </li>
          <li>
            We have the right to suspend or terminate your account and refuse any and all current or future use of the
            Service for any reason at any time. Termination will result in the deletion of your account or your access
            to your account. We have this clause because, statistically speaking, someone out there is doing something
            nefarious, and this clause is how we protect the Service and the people who use it.
          </li>
        </ol>
      </Section>

      <Section id="modifications" title="Modifications to the Service">
        <p>
          Sometimes it becomes technically impossible to continue a feature, or we redesign a part of the Service because
          we think it could be better. We reserve the right at any time to modify or discontinue, temporarily or
          permanently, any part of the Service with or without notice.
        </p>
      </Section>

      <Section id="uptime-security-privacy" title="Uptime, security, and privacy">
        <ol>
          <li>
            Your use of the Service is at your sole risk. We provide the Service on an &ldquo;as is&rdquo; and &ldquo;as
            available&rdquo; basis. We do not offer a service-level agreement, but we do take uptime seriously.
          </li>
          <li>
            We limit how many requests anyone can make in a given time, so the Service stays fast for everyone. If your
            usage significantly exceeds that of other visitors, we may slow down or block it.
          </li>
          <li>
            We take measures to protect and secure your data, and we enforce encryption for data sent between your
            browser and our servers. To report a security problem, follow the steps on our{" "}
            <Link href="/contact#security">contact page</Link>.
          </li>
          <li>
            You agree that we may process your data as described in our <Link href="/privacy">Privacy Policy</Link>{" "}
            and for no other purpose.
          </li>
          <li>
            We use third-party hosting partners (Google Cloud and Vercel) to provide the hardware, software, networking
            and storage the Service runs on. The Privacy Policy lists each one and what it handles.
          </li>
        </ol>
      </Section>

      <Section id="content" title="Copyright and content ownership">
        <ol>
          <li>
            Bill text, actions and vote records are works of the United States government, which we republish.{" "}
            The code of each release is published under the Apache-2.0 license at{" "}
            <a href={PUBLIC_REPO_URL}>{PUBLIC_REPO_NAME}</a>; that license, not these Terms, governs your use of the
            code.
          </li>
          <li>
            You give us a limited license to use what you submit (such as your votes) in order to provide the Service to
            you, but we claim no ownership over it.
          </li>
          <li>
            We reserve the right (but not the obligation) in our sole discretion to refuse or remove anything submitted
            to the Service.
          </li>
          <li>
            The Just a Bill name and logo identify the Service. Please ask us before using them in a way that suggests
            we endorse you or your product.
          </li>
        </ol>
      </Section>

      <Section id="features-and-bugs" title="Features and bugs">
        <p>
          We design the Service with care, based on our own experience and the feedback of people who use it. However,
          there is no such thing as a service that pleases everybody. We make no guarantees that the Service will meet
          your specific requirements or expectations.
        </p>
        <p>
          As with any software, the Service inevitably has some bugs. We track the bugs reported to us and work through
          priority ones, especially any related to security, privacy or the accuracy of vote records. Not all reported
          bugs will get fixed and we don&apos;t guarantee a completely error-free Service.
        </p>
      </Section>

      <Section id="api" title="Automated access and the API">
        <p>
          Any use of our API, or other automated access to the Service, is bound by these Terms plus the following:
        </p>
        <ol>
          <li>
            You expressly understand and agree that we are not liable for any damages or losses resulting from your use
            of the API or of third-party products that access data via the API.
          </li>
          <li>
            Abuse or excessively frequent requests may result in the temporary or permanent suspension of your access.
            We, in our sole discretion, will determine abuse or excessive usage. If your usage could or has caused
            downtime, we may cut off access without prior notice.
          </li>
        </ol>
      </Section>

      <Section id="use-restrictions" title="Use restrictions">
        <p>When you use the Service, you acknowledge that you may not:</p>
        <ul>
          <li>Collect or extract information or user data from accounts which do not belong to you.</li>
          <li>Circumvent, disable, or otherwise interfere with security-related features of the Service, including its rate limits.</li>
          <li>
            Trick, defraud, or mislead us or other users, including but not limited to making false reports,
            impersonating another user, or casting votes through automated means.
          </li>
          <li>Upload or transmit (or attempt to upload or to transmit) viruses or any type of malware.</li>
          <li>Interfere with, disrupt, or create an undue burden on the Service or the networks connected to it.</li>
          <li>Harass, annoy, intimidate, or threaten others, including the people who run the Service.</li>
          <li>Use the Service in a manner inconsistent with any applicable laws or regulations.</li>
        </ul>
        <p>
          Accounts found to be in violation of any of the above are subject to cancellation without prior notice. You can
          report a violation through our <Link href="/contact">contact page</Link>.
        </p>
      </Section>

      <Section id="liability" title="Liability">
        <p>We mention liability throughout these Terms but to put it all in one section:</p>
        <p>
          <strong>
            <em>
              You expressly understand and agree that we shall not be liable, in law or in equity, to you or to any
              third party for any direct, indirect, incidental, lost profits, special, consequential, punitive or
              exemplary damages, including, but not limited to, damages for loss of profits, goodwill, use, data or other
              intangible losses (even if we have been advised of the possibility of such damages), resulting from: (i)
              the use or the inability to use the Service; (ii) the cost of procurement of substitute goods and services
              resulting from any goods, data, information or services purchased or obtained or messages received or
              transactions entered into through or from the Service; (iii) unauthorized access to or alteration of your
              transmissions or data; (iv) statements or conduct of any third party on the Service; (v) or any other
              matter relating to these Terms or the Service, whether as a breach of contract, tort (including negligence
              whether active or passive), or any other theory of liability.
            </em>
          </strong>
        </p>
        <p>
          In other words: choosing to use the Service does mean you are making a bet on us. If the bet does not work
          out, that&apos;s on you, not us. We do our darnedest to be as safe a bet as possible. If you choose to use
          Just a Bill, thank you for betting on us.
        </p>
        <p>
          If you have a question about any of these Terms, please <Link href="/contact">contact us</Link>.
        </p>
      </Section>

      <Section id="governing-law" title="Governing law">
        <p>
          The laws of the State of Kansas govern these Terms and any dispute about them or the Service, without regard
          to its conflict-of-law rules. Any such dispute will be heard only in the state or federal courts located in
          Kansas, and you and we agree to those courts&apos; jurisdiction. This doesn&apos;t take away any protection
          the law where you live gives you that can&apos;t be waived by agreement.
        </p>
      </Section>

      <Section id="changes" title="Changes to these Terms">
        <p>Each significant change to these Terms or our Privacy Policy, newest first:</p>
        <PolicyChanges />
      </Section>

      <PolicyAttribution document="Terms of Service" />
    </TrustPage>
  );
}
