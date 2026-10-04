import Link from "next/link";
import { Section, TrustPage } from "@/components/trust/trust-page";
import {
  CONTACT_EMAIL,
  PRIVACY_EMAIL,
  PUBLIC_REPO_NAME,
  PUBLIC_REPO_URL,
  publicRepoUrl,
  trustMetadata,
} from "@/lib/trust";

export const metadata = trustMetadata(
  "/contact",
  "Contact",
  "Email Just a Bill, report a wrong summary, a vote record or a bug, or report a security problem privately.",
);

export default function ContactPage() {
  return (
    <TrustPage
      title="Contact"
      lead="The best way to reach us is by email. The person who runs Just a Bill reads every message."
    >
      <Section id="email" title="Email">
        <p>
          For questions, or anything else about Just a Bill, email{" "}
          <a href={`mailto:${CONTACT_EMAIL}`}>{CONTACT_EMAIL}</a>.
        </p>
      </Section>

      <Section id="report" title="Report a problem">
        <p>
          Found a summary that gets a bill wrong, a vote that looks misrecorded, a representative matched to the wrong
          district, or a bug? Email <a href={`mailto:${CONTACT_EMAIL}`}>{CONTACT_EMAIL}</a>, or{" "}
          <a href={`${PUBLIC_REPO_URL}/issues`}>open an issue</a> on our public code repository on GitHub. Please
          include the page&apos;s address (the link in your browser) and what you expected to see. An issue is public:
          keep your address and anything else personal out of it.
        </p>
      </Section>

      <Section id="security" title="Report a security problem">
        <p>
          Report it privately through GitHub&apos;s{" "}
          <a href={`${PUBLIC_REPO_URL}/security/advisories/new`}>private vulnerability reporting</a> on our public code
          repository, or email <a href={`mailto:${CONTACT_EMAIL}`}>{CONTACT_EMAIL}</a> with &ldquo;Security&rdquo; in
          the subject. Tell us what&apos;s affected, how to reproduce the problem and what you think its impact is, and
          please give us time to fix it before you tell anyone else. Please don&apos;t open a public issue for it.
        </p>
      </Section>

      <Section id="privacy" title="Questions about your data">
        <p>
          The <Link href="/privacy">Privacy Policy</Link> explains what we keep and how to delete it. If you have a
          question it doesn&apos;t answer, or want to make a privacy request, email{" "}
          <a href={`mailto:${PRIVACY_EMAIL}`}>{PRIVACY_EMAIL}</a>.
        </p>
      </Section>

      <Section id="contribute" title="Contribute">
        <p>
          The code of each release is published under the Apache-2.0 license at{" "}
          <a href={PUBLIC_REPO_URL}>{PUBLIC_REPO_NAME}</a>. To suggest a change, read{" "}
          <a href={publicRepoUrl("CONTRIBUTING.md")}>how to contribute</a>.
        </p>
      </Section>
    </TrustPage>
  );
}
