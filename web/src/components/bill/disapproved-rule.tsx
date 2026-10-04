"use client";

import { useState } from "react";
import Link from "next/link";
import type { DisapprovedRule, FRDocument } from "@/lib/types";
import {
  abstractPreview,
  CRA_EFFECT_URL,
  CRA_METHODOLOGY_HREF,
  docketUrl,
  documentKind,
} from "@/lib/disapproved-rule";
import { CollapsibleCard } from "@/components/ui/collapsible-card";
import { Button } from "@/components/ui/button";
import { formatDate } from "@/lib/utils";

export const DISAPPROVED_RULE_TITLE = "The rule this resolution disapproves";

const LINK = "text-link underline underline-offset-2";
const EXTERNAL = { target: "_blank", rel: "noopener noreferrer" } as const;

interface DisapprovedRuleCardProps {
  rule: DisapprovedRule;
}

/**
 * The rule a Congressional Review Act resolution disapproves (docs/design/590-cra-disapproved-rules.md):
 * the Federal Register document it was matched to, or a plain statement that it couldn't be matched
 * and a search for it. Everything shown comes from the Federal Register, not AI, so there's no AI
 * label; the text is plain text, rendered as text.
 */
export function DisapprovedRuleCard({ rule }: DisapprovedRuleCardProps) {
  const doc = rule.status === "matched" ? rule.document : null;
  return (
    <CollapsibleCard
      title={DISAPPROVED_RULE_TITLE}
      summary={doc ? doc.title : "Not matched to a Federal Register document"}
      contentClassName="space-y-4"
    >
      {doc ? <Matched rule={rule} doc={doc} /> : <Unmatched rule={rule} />}
    </CollapsibleCard>
  );
}

function Matched({ rule, doc }: { rule: DisapprovedRule; doc: FRDocument }) {
  const docket = docketUrl(doc.docket_id);
  return (
    <>
      <p className="text-xs text-muted-foreground">From the Federal Register</p>
      <DocumentHeading doc={doc} />

      <div className="space-y-2">
        <p className="text-sm text-foreground">Abstract, as published by the agency:</p>
        <DocumentAbstract abstract={doc.abstract} />
      </div>

      <ul className="space-y-1 text-sm">
        {doc.html_url && (
          <li>
            <a href={doc.html_url} {...EXTERNAL} className={LINK}>
              Read the rule on FederalRegister.gov
            </a>
          </li>
        )}
        {doc.pdf_url && (
          <li>
            <a href={doc.pdf_url} {...EXTERNAL} className={LINK}>
              Official PDF (GovInfo)
            </a>
          </li>
        )}
        {docket && (
          <li>
            <a href={docket} {...EXTERNAL} className={LINK}>
              Docket {doc.docket_id} on Regulations.gov
            </a>
          </li>
        )}
      </ul>

      {rule.withdrawn_document && (
        <div className="space-y-2 rounded-md border border-border p-3">
          <p className="text-sm font-medium text-foreground">This document withdrew:</p>
          <DocumentHeading doc={rule.withdrawn_document} />
          <DocumentAbstract abstract={rule.withdrawn_document.abstract} />
        </div>
      )}

      {rule.method === "title" && (
        <p role="note" className="text-xs text-muted-foreground">
          {rule.cited
            ? `We matched this document by its title, agency and date. The resolution's text cites ${rule.cited}, ` +
              "a different document."
            : "The resolution doesn't cite a Federal Register page. We matched this document by its title, agency " +
              "and date."}{" "}
          <Link href={CRA_METHODOLOGY_HREF} className="underline underline-offset-2 hover:text-foreground">
            How we match
          </Link>
        </p>
      )}

      <CraEffect />
    </>
  );
}

function Unmatched({ rule }: { rule: DisapprovedRule }) {
  return (
    <>
      <p className="text-sm text-foreground">
        We couldn&apos;t match this resolution to a Federal Register document, so we don&apos;t show one.
      </p>
      {rule.gao_opinion && (
        <p className="text-sm text-foreground">
          The resolution identifies this action by its issue date and a Government Accountability Office opinion
          that it is a rule, not by a Federal Register citation.
        </p>
      )}
      <p className="text-sm">
        <a href={rule.search_url} {...EXTERNAL} className={LINK}>
          Search the Federal Register for &ldquo;{rule.rule_title}&rdquo;
        </a>
      </p>
      <p className="text-xs text-muted-foreground">
        <Link href={CRA_METHODOLOGY_HREF} className="underline underline-offset-2 hover:text-foreground">
          How we match
        </Link>
      </p>
    </>
  );
}

/** The document's title (linked to FederalRegister.gov), its agencies, and its kind, dates and citation. */
function DocumentHeading({ doc }: { doc: FRDocument }) {
  const facts = [
    documentKind(doc),
    `Published ${formatDate(doc.publication_date, "long")}`,
    doc.citation,
    doc.effective_on && `Effective ${formatDate(doc.effective_on, "long")}`,
  ].filter(Boolean);
  return (
    <div className="space-y-1">
      <p className="font-medium leading-snug text-foreground">
        {doc.html_url ? (
          <a href={doc.html_url} {...EXTERNAL} className={LINK}>
            {doc.title}
          </a>
        ) : (
          doc.title
        )}
      </p>
      {doc.agencies.length > 0 && <p className="text-sm text-muted-foreground">{doc.agencies.join(", ")}</p>}
      <p className="text-xs text-muted-foreground">{facts.join(" · ")}</p>
    </div>
  );
}

/** The agency's abstract, quoted, with "Read more" past about ABSTRACT_PREVIEW_CHARS characters. */
function DocumentAbstract({ abstract }: { abstract: string | null }) {
  const [expanded, setExpanded] = useState(false);
  if (!abstract?.trim()) {
    return <p className="text-sm text-muted-foreground">The Federal Register entry has no abstract.</p>;
  }
  const { preview, truncated } = abstractPreview(abstract);
  return (
    <>
      <blockquote className="border-l-2 border-border pl-3 text-sm leading-relaxed text-foreground whitespace-pre-wrap">
        {expanded ? abstract.trim() : preview}
      </blockquote>
      {truncated && (
        <Button variant="ghost" size="sm" onClick={() => setExpanded(!expanded)} aria-expanded={expanded}>
          {expanded ? "Show less" : "Read more"}
        </Button>
      )}
    </>
  );
}

/** What the Congressional Review Act says a disapproval does, linking 5 U.S.C. 801. */
function CraEffect() {
  return (
    <p className="text-xs text-muted-foreground">
      Under the{" "}
      <a href={CRA_EFFECT_URL} {...EXTERNAL} className="underline underline-offset-2 hover:text-foreground">
        Congressional Review Act
      </a>
      , if this resolution becomes law, the rule has no force or effect, and the agency can&apos;t issue a
      substantially similar rule unless a later law authorizes it.
    </p>
  );
}
