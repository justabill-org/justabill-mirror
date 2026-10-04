"use client";

import { useState } from "react";
import type { CrsSummary as CrsSummaryType } from "@/lib/types";
import { crsPreview } from "@/lib/crs";
import { CollapsibleCard } from "@/components/ui/collapsible-card";
import { Button } from "@/components/ui/button";
import { formatDate } from "@/lib/utils";

interface CrsSummaryProps {
  summary: CrsSummaryType;
  /** This summary on Congress.gov (crsSummaryUrl). */
  summaryUrl?: string;
  /** The bill has a text version newer than the action the summary describes (billChangedSince). */
  changedSince?: boolean;
  /** Inside another section (the disclosure under the AI summary, #664): no card and no heading. */
  embedded?: boolean;
}

/**
 * The Congressional Research Service's summary (docs/design/197-crs-summaries.md). The text is plain
 * text rendered as text, never as HTML.
 */
export function CrsSummary({ summary, summaryUrl, changedSince = false, embedded = false }: CrsSummaryProps) {
  const [expanded, setExpanded] = useState(false);
  const { preview, truncated } = crsPreview(summary.text);
  const actionDate = formatDate(summary.action_date, "long");

  const content = (
    <>
      <p className="text-xs text-muted-foreground">
        Summary of the bill as <em>{summary.action_desc}</em> · {actionDate}
      </p>
      {changedSince && (
        <p role="note" className="text-xs text-muted-foreground">
          The bill has changed since this summary was written.
        </p>
      )}

      <p className="text-sm text-foreground leading-relaxed whitespace-pre-wrap">
        {expanded ? summary.text.trim() : preview}
      </p>

      {truncated && (
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setExpanded(!expanded)}
          aria-expanded={expanded}
        >
          {expanded ? "Show less" : "Read more"}
        </Button>
      )}

      {summaryUrl && (
        <p className="text-xs text-muted-foreground">
          <a
            href={summaryUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="underline underline-offset-2 hover:text-foreground"
          >
            Read this summary on Congress.gov
          </a>
        </p>
      )}
    </>
  );

  if (embedded) return <div className="space-y-4">{content}</div>;

  return (
    <CollapsibleCard
      title={
        <>
          Official summary
          <span className="mt-1 block text-sm font-normal tracking-normal text-muted-foreground">
            Congressional Research Service
          </span>
        </>
      }
      defaultOpenNarrow
      contentClassName="space-y-4"
    >
      {content}
    </CollapsibleCard>
  );
}
