"use client";

import { useState } from "react";
import Link from "next/link";
import type { BillSummary as BillSummaryType } from "@/lib/types";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { CollapsibleCard } from "@/components/ui/collapsible-card";
import { Button } from "@/components/ui/button";
import { SummaryProvenance } from "./summary-provenance";
import { formatDate } from "@/lib/utils";

interface BillSummaryProps {
  summary: BillSummaryType | null;
  /** The bill's official text on Congress.gov (congressGovTextUrl), linked under the summary. */
  officialTextUrl?: string;
}

export function BillSummary({ summary, officialTextUrl }: BillSummaryProps) {
  const [expanded, setExpanded] = useState(false);

  if (!summary) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="text-lg">Summary</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            No AI summary available for this bill yet.
          </p>
        </CardContent>
      </Card>
    );
  }

  const whoItAffects = summary.who_it_affects;

  return (
    <CollapsibleCard
      title="AI Summary"
      summary={summary.short_summary}
      defaultOpenNarrow
      contentClassName="space-y-4"
    >
        {/* AI disclaimer (#75): shown before the summary, not after it */}
        <p role="note" className="rounded-md border border-border bg-muted/50 px-3 py-2 text-xs text-muted-foreground">
          {disclaimer(summary)}{" "}
          It may contain errors.{" "}
          <Link href="/methodology#summaries" className="underline underline-offset-2 hover:text-foreground">
            How summaries are made
          </Link>
        </p>

        {/* Short summary - always visible */}
        {summary.short_summary && (
          <p className="text-foreground leading-relaxed">
            {summary.short_summary}
          </p>
        )}

        {/* Expandable long summary */}
        {summary.long_summary && (
          <>
            {expanded && (
              <div className="space-y-3 border-t border-border pt-4">
                <p className="text-sm text-muted-foreground leading-relaxed whitespace-pre-wrap">
                  {summary.long_summary}
                </p>
              </div>
            )}
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setExpanded(!expanded)}
              aria-expanded={expanded}
            >
              {expanded ? "Show less" : "Read more"}
              <ChevronIcon className={`ml-1 h-4 w-4 transition-transform ${expanded ? "rotate-180" : ""}`} />
            </Button>
          </>
        )}

        {/* Who it affects */}
        {whoItAffects && (
          <div className="rounded-lg border border-border bg-muted/40 p-4">
            <h4 className="text-sm font-semibold text-foreground mb-2">Who it affects</h4>
            <p className="text-sm text-foreground leading-relaxed">
              {whoItAffects}
            </p>
          </div>
        )}

        {/* The text version and model it's based on; the model name appears only here */}
        <SummaryProvenance summary={summary} />

        {/* Generation date and the official text */}
        {(summary.generated_at || officialTextUrl) && (
          <p className="text-xs text-muted-foreground">
            {summary.generated_at && (
              <>
                Generated {formatDate(summary.generated_at, "long")}
                {officialTextUrl && " · "}
              </>
            )}
            {officialTextUrl && (
              <a
                href={officialTextUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="underline underline-offset-2 hover:text-foreground"
              >
                Read the official text on Congress.gov
              </a>
            )}
          </p>
        )}
    </CollapsibleCard>
  );
}

/**
 * The disclaimer's first sentence: what the model was given besides the bill text, the CRS summary
 * (bill-v3) and a CRA resolution's rule from the Federal Register (bill-v4, #643).
 */
function disclaimer(summary: BillSummaryType): string {
  const context = [
    summary.with_crs_summary && "the Congressional Research Service summary",
    summary.with_rule_context && "the Federal Register's description of the rule",
  ].filter(Boolean);
  if (context.length === 0) return "AI-generated from the official bill text and not reviewed by a person.";
  return `AI-generated from the official bill text, with ${context.join(" and ")} as context, and not reviewed by a person.`;
}

function ChevronIcon({ className }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2} aria-hidden="true">
      <path strokeLinecap="round" strokeLinejoin="round" d="M19.5 8.25l-7.5 7.5-7.5-7.5" />
    </svg>
  );
}
