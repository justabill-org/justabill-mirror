import Link from "next/link";
import { BASECAMP_POLICIES_URL, CC_BY_URL, POLICY_CHANGES } from "@/lib/trust";

interface TrustPageProps {
  title: string;
  /** One sentence under the title. */
  lead: string;
  /** Shown as "Last updated …" on the policies. */
  lastUpdated?: string;
  children: React.ReactNode;
}

/** The frame of every trust page (#75): a title, a lead, and prose styled for reading. */
export function TrustPage({ title, lead, lastUpdated, children }: TrustPageProps) {
  return (
    <article className="mx-auto max-w-2xl px-4 py-10 sm:px-6 lg:px-8">
      <header className="mb-8 border-b border-border pb-6">
        <h1 className="text-3xl font-semibold tracking-tight text-foreground sm:text-4xl">{title}</h1>
        <p className="mt-3 text-lg text-muted-foreground">{lead}</p>
        {lastUpdated && <p className="mt-3 text-sm text-muted-foreground">Last updated: {lastUpdated}</p>}
      </header>
      <div className="space-y-6 leading-relaxed text-foreground [&_a]:text-link [&_a]:underline [&_a]:underline-offset-2 [&_h2]:mt-10 [&_h2]:text-2xl [&_h2]:font-semibold [&_h2]:tracking-tight [&_h3]:mt-6 [&_h3]:font-semibold [&_li]:mt-2 [&_ol]:list-decimal [&_ol]:pl-6 [&_ul]:list-disc [&_ul]:pl-6">
        {children}
      </div>
    </article>
  );
}

/** A section with an anchor, so other pages can link to it (e.g. /methodology#summaries). */
export function Section({ id, title, children }: { id: string; title: string; children: React.ReactNode }) {
  return (
    <section id={id} aria-labelledby={`${id}-title`} className="scroll-mt-20 space-y-4">
      <h2 id={`${id}-title`}>{title}</h2>
      {children}
    </section>
  );
}

/** The policies' change history (POLICY_CHANGES), newest first, one line per change (#806). */
export function PolicyChanges() {
  return (
    <ul aria-label="Changes to our policies">
      {POLICY_CHANGES.map(({ date, change }) => (
        <li key={`${date} ${change}`}>
          <strong>{date}:</strong> {change}
        </li>
      ))}
    </ul>
  );
}

/**
 * The CC BY 4.0 notice on /privacy and /terms: names Basecamp (37signals), links the source and
 * the license, and says the text was changed, as the license's section 3(a) requires.
 */
export function PolicyAttribution({ document }: { document: "Privacy Policy" | "Terms of Service" }) {
  return (
    <aside className="mt-12 rounded-lg border border-border bg-muted/40 p-4 text-sm text-muted-foreground">
      {document === "Privacy Policy" ? "This Privacy Policy is" : "These Terms of Service are"} adapted from the{" "}
      <a href={BASECAMP_POLICIES_URL} target="_blank" rel="noopener noreferrer">
        Basecamp (37signals) policies
      </a>
      , used under{" "}
      <a href={CC_BY_URL} target="_blank" rel="noopener noreferrer">
        CC BY 4.0
      </a>
      . We changed the text: we removed what doesn&apos;t apply to Just a Bill (payments, their products, their
      subprocessors) and rewrote the rest to describe our service. 37signals doesn&apos;t endorse Just a Bill. Our
      adapted text is available under the same license. Questions: <Link href="/contact">contact us</Link>.
    </aside>
  );
}
