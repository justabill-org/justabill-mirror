import Link from "next/link";
import { PolicyAttribution, PolicyChanges, Section, TrustPage } from "@/components/trust/trust-page";
import { providerNames } from "@/lib/auth/providers";
import { POLICIES_LAST_UPDATED, PRIVACY_EMAIL, trustMetadata } from "@/lib/trust";

export const metadata = trustMetadata(
  "/privacy",
  "Privacy Policy",
  "What Just a Bill collects and why: votes stay in your browser unless you sign in, addresses aren't saved, and " +
    "there are no ads or tracking cookies.",
);

// Adapted from Basecamp's Privacy Policy (github.com/basecamp/policies, CC BY 4.0, commit d39f6f1).
// Every claim here must match what the code does; check it against the code when either changes:
// - browser storage: web/src/lib/local (jab.votes.v1, jab.reps.v1, #214; the Vote page's filter is in its URL, #797)
// - accounts: users, user_votes and user_favorites in db/schema.sql; /me routes in api/cmd/server
//   (GET /me/export returns every stored column: UserRepository.Export in db/spannerdb/users.go); the
//   sign-in providers named are the build's NEXT_PUBLIC_AUTH_PROVIDERS (lib/auth/providers.ts, #652)
// - address lookup: api/internal/district, POST /reps (the address is only in the body, never logged; #268)
// - use my location: components/scorecard/find-reps-form.tsx asks only on the button; lat and lon go in the
//   same POST /reps body (#711), and Permissions-Policy allows geolocation for this origin only (#668)
// - analytics: web/src/lib/analytics.ts; logs and retention: docs/design/28-production.md and 53
// - third-party scripts: the CSP in web/src/lib/security-headers.ts; reCAPTCHA through App Check
//   loads only for signed-in users, from web/src/lib/auth (#122, docs/design/89-aggregate-analytics.md);
//   the Firebase sign-in library (and with it Google's helper) loads only after a sign-in press, or when
//   the jab.signed-in.v1 / jab.sign-in-redirect.v1 notes say someone signed in here (lib/auth/session-hint.ts, #757)
// - browser performance and error data: web/src/lib/obs/browser.ts, browser-sdk.ts and relay.ts (#292)
// - sharing: web/src/lib/share.ts (what a link holds), components/share/share-dialog.tsx (when it's
//   fetched), redactSharePath and trackShare in web/src/lib/analytics.ts; docs/design/88-share-cards.md;
//   a bill's own link: billLinkUrl and billLinkText in web/src/lib/share-links.ts (#810)
// - layout tests: the jab_exp cookie in web/src/lib/experiments/assign.ts and web/src/proxy.ts (only
//   while a test runs), the jab.exp.v1 note and beacons in web/src/lib/experiments/client.tsx, and the
//   counts in web/src/lib/experiments/endpoint.ts; docs/design/580-ab-experiments.md
// The children's and state-privacy sections (#331): the terms' minimum age (13) must match the
// children's section.
export default function PrivacyPage() {
  return (
    <TrustPage
      title="Privacy Policy"
      lead="The privacy of your data (and it is your data, not ours!) is a big deal to us."
      lastUpdated={POLICIES_LAST_UPDATED}
    >
      <p>
        In this policy, we lay out what data we collect and why, how it is handled, and your rights over it. How you
        vote on bills is sensitive, so we built Just a Bill to need as little of it as possible. We promise we never
        sell your data: never have, never will.
      </p>

      <Section id="summary" title="The short version">
        <ul>
          <li>
            <strong>Without an account, your votes stay in your browser.</strong> They aren&apos;t sent to us unless
            you choose to <a href="#sharing">share one</a>.
          </li>
          <li>
            <strong>We don&apos;t save your address or location.</strong> We use it once to find your district and keep
            only the state and district.
          </li>
          <li>
            <strong>If you sign in</strong>, we store your votes, followed bills, state and district. Your name and email
            stay with the sign-in service and never reach our database.
          </li>
          <li>
            <strong>No ads, no tracking cookies, no data sales.</strong> Our page-view counts are cookieless. We use
            one cookie only while we <a href="#layout-tests">test two layouts of a page</a>, and it doesn&apos;t identify you.
          </li>
        </ul>
      </Section>

      <Section id="what-we-collect" title="What we collect and why">
        <h3 id="without-an-account">Using Just a Bill without an account</h3>
        <p>
          You can read bills, vote on them and compare your votes with your representatives&apos; without signing in.
          Your votes (and the title of each bill you voted on) are saved in your browser&apos;s local storage, on your
          device only. So are your state, district and the list of your representatives once you look them up, and,
          while we&apos;re <a href="#layout-tests">testing two layouts of a page</a>, a note that your browser has already
          told us it saw the page or took the action we&apos;re comparing. To
          compare your votes, your browser downloads your representatives&apos; public voting records and does the
          comparison itself, so your votes aren&apos;t sent to us. Clearing your browser&apos;s site data erases all of
          it.
        </p>

        <h3 id="sharing">Sharing</h3>
        <p>
          You can share how you&apos;d vote on a bill as a card with a link, alone or next to one of your
          representatives&apos; recorded votes on it. Nothing about a card is sent until you open the share dialog.
          Then your browser loads the card&apos;s image from our website, and the card&apos;s web address holds only
          what the card shows: the bill, your vote on that one bill and, if you pick one, the member. It doesn&apos;t
          include your other votes, your address, your account or anything else about you.
        </p>
        <p>
          You can also share how Just a Bill users voted on a bill, nationwide or in one state or district. That
          card&apos;s web address holds only the bill and the place, and never your own vote. If the place you share is
          your own state or district, anyone with the link can see that place.
        </p>
        <p>
          You can also share a bill itself. Its link is the bill&apos;s page on our website, and the text that goes
          with it is the bill&apos;s number and title. Neither holds your vote, your address, your account or the page
          you shared it from. When you share a bill, we count how you shared it (for example, copying the link) along
          with the page you were on, the same way we count page views.
        </p>
        <p>
          We don&apos;t save cards or links in our database. Like any page, a card&apos;s web address appears in our
          hosting providers&apos; request logs. Our page-view counts record only the kind of card, never the vote or
          counts on it, and when you share one we count only the kind of card and how you shared it (for example,
          copying the link). Anyone with the link can see the card, and so can the apps and sites you post it to, which
          show a preview. Share pages ask search engines not to list them.
        </p>

        <h3 id="address">Finding your representatives</h3>
        <p>
          When you enter an address to find your district, your browser sends it to our API, which passes it to the{" "}
          <a href="https://geocoding.geo.census.gov/" target="_blank" rel="noopener noreferrer">
            US Census Bureau&apos;s geocoder
          </a>{" "}
          and gets back your state and congressional district. We don&apos;t save the address in our database. We keep
          only the state and district: in your browser, or in your account if you&apos;re signed in. The address travels
          in the body of the request, not in the web address, so it stays out of our request logs and our hosting
          providers&apos; logs.
        </p>
        <p>
          If you choose &ldquo;Use my location&rdquo; instead, your browser asks your permission first. If you allow
          it, your browser sends your latitude and longitude to our API the same way, and our API passes them to the
          same geocoder to find your district. We don&apos;t save them or log them, and we ask for your location only
          when you press the button. You can turn location off for this site in your browser&apos;s settings at any
          time.
        </p>

        <h3 id="accounts">Accounts</h3>
        <p>
          Accounts are optional, and while we&apos;re testing them they may not be open to everyone. You sign in with an
          existing {providerNames()} account through Google Cloud Identity Platform. That service receives your
          name and email address from the account you choose; they stay there and never reach our database. We
          don&apos;t send you email.
        </p>
        <p>For a signed-in account, our database stores:</p>
        <ul>
          <li>a random account ID, the ID Identity Platform gives you, and which kind of account you signed in with;</li>
          <li>your state and congressional district, and when you last changed your district;</li>
          <li>your votes on bills (yea, nay or skip) and when you cast them;</li>
          <li>the bills you follow, and when you followed them;</li>
          <li>
            for each vote, whether it passed our automated check that requests come from our own app, when that check is
            turned on.
          </li>
        </ul>
        <p>
          We use these to show your votes on every device you sign in on, to build your scorecard, and to publish
          rounded totals of how users voted (see{" "}
          <a href="#access-and-disclosure">aggregated data</a>). When you sign in,
          you can choose to copy the votes saved in your browser into your account. To stop abuse, we limit how many
          votes an account can cast in a day and how often it can change its district.
        </p>
        <p id="recaptcha">
          The automated check is Google&apos;s{" "}
          <a href="https://firebase.google.com/docs/app-check" target="_blank" rel="noopener noreferrer">
            Firebase App Check
          </a>{" "}
          with{" "}
          <a href="https://cloud.google.com/security/products/recaptcha" target="_blank" rel="noopener noreferrer">
            reCAPTCHA
          </a>
          . It loads in your browser only while you&apos;re signed in, never for visitors who aren&apos;t. reCAPTCHA
          collects information about your browser and device and how you use the page, including your IP address, and
          sends it to Google, which uses it to tell people from automated scripts. When you vote or copy votes into your
          account, your browser sends us a short-lived token from App Check, and we keep only whether it was valid. We
          never receive or store what reCAPTCHA collected or the score Google gave it. Google&apos;s{" "}
          <a href="https://policies.google.com/privacy" target="_blank" rel="noopener noreferrer">
            Privacy Policy
          </a>{" "}
          and{" "}
          <a href="https://policies.google.com/terms" target="_blank" rel="noopener noreferrer">
            Terms of Service
          </a>{" "}
          apply to that information.
        </p>

        <h3 id="website-interactions">Website interactions</h3>
        <p>
          We count page views with{" "}
          <a href="https://vercel.com/docs/analytics/privacy-policy" target="_blank" rel="noopener noreferrer">
            Vercel Web Analytics
          </a>
          , which doesn&apos;t use cookies. It tells visits apart with a hash of the request that it discards after 24
          hours, and it doesn&apos;t store IP addresses. We send it only the page&apos;s path and any <code>ref</code> or{" "}
          <code>utm_</code> campaign tags in the link; your browser removes search text and every other part of the web
          address before sending. It records the referring site, your approximate location (country, region and city),
          operating system, browser and device type.
        </p>
        <p>
          Like almost every website, our hosting providers keep request logs: the web address requested, the time, your
          IP address and your browser&apos;s user agent. Our own servers log each request&apos;s path, result and timing,
          and some events (such as creating or deleting an account) with the random account ID, never your name or
          email. We use logs to keep the service running and to investigate abuse.
        </p>
        <p>
          To find what&apos;s slow or broken, your browser sends us anonymous performance and error data: how long
          pages and our API took to respond, which kind of page it was (such as &ldquo;a bill page&rdquo;, not which
          bill), and the details of any error the site hit. It carries no account ID, cookie or other identifier, no
          search text or other parts of the web address after the page type, and never how you voted. It goes to our
          own servers, which drop anything outside that list before storing it with the rest of our logs. We keep a
          sample of the performance data and every error report (at most five per page view).
        </p>
        <p>
          To keep the service fast for everyone, our API limits how many requests each IP address can make per minute,
          including the requests our website&apos;s server makes while showing you a page, for which it passes your IP
          address to our API.
          It keeps those counts in memory for about a minute.
        </p>

        <h3 id="cookies">Advertising and cookies</h3>
        <p>
          Just a Bill doesn&apos;t show ads and doesn&apos;t use advertising or tracking cookies, pixels or other
          third-party trackers. If you don&apos;t sign in, no third-party scripts or fonts load in your browser. When you
          sign in, Google&apos;s sign-in helper loads from apis.google.com, and the sign-in library keeps you signed in
          using your browser&apos;s storage. We also keep a note there that someone signed in on this device, so the
          sign-in library loads only after a sign-in, and remove it when you sign out. While you&apos;re signed in,
          Google reCAPTCHA also loads, as described{" "}
          <a href="#recaptcha">under Accounts</a>.
        </p>
        <p id="layout-tests">
          Sometimes we test two versions of a page&apos;s layout to learn which one is easier to use. While a test
          runs, we set one cookie that holds which version you were shown (for example
          &ldquo;vote-deck-layout.treatment&rdquo;), so you keep seeing the same one. It holds nothing about you,
          isn&apos;t linked to your account, and expires when the test ends, within 30 days. Your browser tells us once
          that you saw the page, and once if you took the action we&apos;re comparing (such as voting on a bill, never
          how you voted). We count those only as totals for each version. We don&apos;t test anything that could change
          what you learn about a bill, member or party. If your browser sends a Global Privacy Control signal, we always
          show the usual version, set no cookie and count nothing.
        </p>

        <h3 id="correspondence">Voluntary correspondence</h3>
        <p>
          When you email us, for example to report a problem, we keep your message, your email address and our reply
          in our mailbox, and use them only to answer you. Ask us to delete your messages and we will.
        </p>
        <p>
          If you instead open an issue or a pull request on our public code repository on GitHub, it&apos;s public:
          anyone can read it, along with your GitHub username. GitHub hosts it under its own terms and privacy
          statement, not this policy.
        </p>
      </Section>

      <Section id="service-providers" title="Who else handles your data">
        <ul>
          <li>
            <strong>Vercel</strong> hosts the website and provides our page-view counts. Its runtime logs hold IP
            addresses and the web addresses requested, and it keeps them for one day.
          </li>
          <li>
            <strong>Google Cloud</strong> runs our API and database. Its load balancer logs hold IP addresses and the web
            addresses requested.
          </li>
          <li>
            <strong>Google Cloud Identity Platform</strong> handles sign-in and holds the name and email address of
            signed-in users.
          </li>
          <li>
            <strong>Google reCAPTCHA and Firebase App Check</strong> check that signed-in users&apos; votes come from
            our app. reCAPTCHA receives information about the browser, the device and how the page is used, including
            the IP address.
          </li>
          <li>
            <strong>Google Workspace</strong> holds the email you send us and our replies.
          </li>
          <li>
            <strong>The US Census Bureau&apos;s geocoder</strong> receives the addresses, or the coordinates from
            &ldquo;Use my location&rdquo;, looked up for district lookup, and nothing else about you.
          </li>
        </ul>
        <p>
          Bill summaries are written by Google&apos;s Gemini models on Vertex AI from the bills&apos; public text. No
          information about you is sent to them.
        </p>
      </Section>

      <Section id="access-and-disclosure" title="When we access or disclose your information">
        <p>
          No one at Just a Bill looks at your data except for limited purposes: to fix an error that stops an automated
          process and needs a person, to investigate abuse (see the{" "}
          <Link href="/terms#use-restrictions">use restrictions</Link>), or when required by law. When that happens, we
          look at as little as we can.
        </p>
        <p>
          <strong>Aggregated and de-identified data.</strong> We publish statistics built from signed-in users&apos;
          votes and districts: how Just a Bill users voted on a bill nationwide, by state and by congressional district,
          and how often users in a district or state agreed with its representatives. We publish them only in forms
          that can&apos;t identify anyone: a place needs at least 50 counted votes (100 nationwide) before anything is
          shown, percentages are whole numbers, the number of users is rounded down to the nearest 10, and a
          place&apos;s numbers change only after at least 10 votes have. The full rules are on the{" "}
          <Link href="/methodology#aggregates">Methodology</Link> page. Votes kept only in your browser are never
          counted.
        </p>
        <p>
          <strong>When required under applicable law.</strong> Just a Bill is operated in the United States, and our
          data is stored there. Our policy is not to respond to government requests for user data unless we are
          compelled by legal process, or in limited circumstances in the event of an emergency request. If US law
          enforcement authorities have the necessary warrant, criminal subpoena, or court order requiring us to disclose
          data, we must comply. It is our policy to notify affected users before we disclose data unless we are legally
          prohibited from doing so, and except in some emergency cases. Signed-out votes are only in your browser, so we
          have none to disclose.
        </p>
        <p>
          Finally, if Just a Bill is taken over by another organization, we&apos;ll post a notice here well before any
          of your personal information is transferred or becomes subject to a different privacy policy.
        </p>
      </Section>

      <Section id="your-rights" title="Your rights with respect to your information">
        <p>We apply the same rights to everyone, wherever they live:</p>
        <ul>
          <li>
            <strong>Right to know.</strong> This policy lists everything we collect and why.
          </li>
          <li>
            <strong>Right of access and portability.</strong> Signed in, use &ldquo;Download my data&rdquo; in Settings
            to get a file with everything our database holds about your account.
          </li>
          <li>
            <strong>Right to correction.</strong> Change your district in Settings, and change or remove a vote at any
            time.
          </li>
          <li>
            <strong>Right to erasure.</strong> &ldquo;Delete my account&rdquo; in Settings deletes your account, votes
            and followed bills from our database right away, and your sign-in record from Identity Platform. Signed out,
            clearing your browser&apos;s site data for Just a Bill erases your votes.
          </li>
          <li>
            <strong>Right to non-discrimination.</strong> Just a Bill is free for everyone, and exercising these rights
            doesn&apos;t change that.
          </li>
        </ul>
        <p>
          If you have questions about exercising these rights or need assistance, <Link href="/contact">contact us</Link>
          .
        </p>
      </Section>

      <Section id="state-privacy-rights" title="US state privacy rights">
        <p>
          California (under the CCPA, as amended by the CPRA), Colorado, Connecticut, Virginia and a growing number of
          other states give their residents privacy rights. Many of these laws apply only to organizations above a
          certain size or revenue, and Just a Bill, which is free and earns nothing, may not be covered by them. We
          give everyone the rights above anyway, and we describe our practices here in the terms these laws use.
        </p>
        <ul>
          <li>
            <strong>What we collect.</strong> Identifiers (a random account ID, the ID from our sign-in provider, and
            IP addresses in request logs), internet activity (the pages and API requests in our logs), approximate
            location (your state and congressional district, and the country, region and city in our page-view counts)
            and, if you sign in, your votes on bills and the bills you follow.{" "}
            <a href="#what-we-collect">What we collect and why</a> gives the
            details, and <a href="#retention">Data retention</a> says how long we keep each.
          </li>
          <li>
            <strong>We don&apos;t sell or share your personal information</strong>, and we haven&apos;t in the past 12
            months. We don&apos;t use it for targeted advertising, and we don&apos;t make automated decisions about
            you that have legal or similarly significant effects. There&apos;s nothing to opt out of, so we treat
            every visitor as opted out, including browsers that send a Global Privacy Control signal.
          </li>
          <li>
            <strong>Your votes are sensitive.</strong> How you vote on bills can reveal your political views, and some
            laws treat that as sensitive information. We store your votes only if you sign in and choose to save them
            to your account, and we use them only to provide the Service to you: to show them back to you and build
            your scorecard.
          </li>
          <li>
            <strong>Making a request.</strong> <a href="#your-rights">Your rights</a> above says how to download,
            correct or delete your data yourself. For anything else, email <a href={`mailto:${PRIVACY_EMAIL}`}>{PRIVACY_EMAIL}</a>.
            We&apos;ll confirm the request comes from you (for account data, by having you sign in), and we&apos;ll
            answer within 45 days. Someone you authorize can make a request for you; we&apos;ll ask them for proof that
            you did.
          </li>
          <li>
            <strong>Appeals.</strong> If we turn down your request, you can ask us to reconsider by replying to our
            answer, and we&apos;ll answer your appeal within 60 days. If we still turn it down, you can contact your
            state&apos;s attorney general.
          </li>
        </ul>
      </Section>

      <Section id="children" title="Children">
        <p>
          Just a Bill isn&apos;t directed to children under 13, and you must be 13 or older to create an account (see
          our <Link href="/terms#accounts">Terms of Service</Link>). We don&apos;t knowingly collect personal
          information from children under 13. If you believe a child under 13 has created an account,{" "}
          <Link href="/contact#privacy">contact us</Link> and we&apos;ll delete it. Anyone can read bills and vote on
          them without an account; those votes stay in the browser.
        </p>
      </Section>

      <Section id="security" title="How we secure your data">
        <p>
          All data is encrypted with TLS when it travels between your browser and our servers, and our database and its
          backups are encrypted at rest by Google Cloud. To report a security problem, see our <Link href="/contact#security">contact page</Link>.
        </p>
      </Section>

      <Section id="retention" title="Data retention">
        <ul>
          <li>Votes and lookups saved in your browser: until you clear them.</li>
          <li>The layout test cookie: until the test ends, within 30 days.</li>
          <li>
            Account data: until you delete your account. After that, copies remain only in our database backups, which
            expire within 14 days.
          </li>
          <li>Vercel runtime logs: one day. Page-view hashes: 24 hours.</li>
          <li>Email you send us: until you ask us to delete it.</li>
          <li>Google Cloud request logs, our server logs, and browser performance and error data: 30 days.</li>
        </ul>
      </Section>

      <Section id="location" title="Location of site and data">
        <p>
          Just a Bill is operated in the United States, and its database is in a Google Cloud region there. If you are
          located outside the United States, <strong>please be aware that any information you provide to us will be
          transferred to and stored in the United States</strong>.
        </p>
      </Section>

      <Section id="changes" title="Changes and questions">
        <p>
          We may update this policy as needed to comply with relevant regulations and reflect any new practices. Each
          significant change is dated on this page: we refresh the date at the top and list the change at the end of
          this section, newest first.
        </p>
        <p>
          Have any questions, comments, or concerns about this privacy policy, your data, or your rights with respect to
          your information? Please <Link href="/contact">get in touch</Link> and we&apos;ll be happy to try to answer
          them!
        </p>
        <PolicyChanges />
      </Section>

      <PolicyAttribution document="Privacy Policy" />
    </TrustPage>
  );
}
