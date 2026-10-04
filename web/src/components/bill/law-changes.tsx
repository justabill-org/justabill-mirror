import Link from "next/link";
import type { BillLawChangesResponse, LawChangeEntry } from "@/lib/types";
import { billLabel } from "@/lib/graph";
import { LAW_CHANGE_KIND_LABELS, lawCitation, publicLawName, uscodeHouseUrl } from "@/lib/law-changes";
import { Badge } from "@/components/ui/badge";
import { CollapsibleCard } from "@/components/ui/collapsible-card";
import { LawSectionText } from "./law-section-text";
import { formatDate } from "@/lib/utils";

interface LawChangesProps {
  /** GET /bills/{id}/law-changes, or null when it failed. */
  data: BillLawChangesResponse | null;
  /** The Congress.gov name of the text version the list describes ("Reported in House"). */
  versionName?: string;
}

/**
 * "Changes to current law" (#316, docs/design/149-law-aware-assistant.md): each US Code section (or
 * statutory note under one, #573) the bill's latest text amends, repeals or adds, with the AI
 * explanation, the bill's own instruction, the section's current text on demand, and other bills
 * changing the same section.
 * Renders nothing when the list is empty or the request failed.
 */
export function LawChanges({ data, versionName }: LawChangesProps) {
  if (!data || data.changes.length === 0) return null;

  const current = data.current_release_point?.release_point;
  const explainedAt = data.explained?.release_point;
  const version = versionName || data.version_code;

  return (
    <section className="mt-8" aria-label="Changes to current law">
      <CollapsibleCard
        title="Changes to current law"
        summary={
          data.changes.length === 1 ? "1 section of law" : `${data.changes.length} sections of law`
        }
        contentClassName="space-y-4"
      >
          <p className="text-sm text-muted-foreground">
            Sections of law the bill{version ? <>&apos;s <em>{version}</em> text</> : "'s latest text"} would
            amend, repeal or add to, found in the bill&apos;s own text.
            {current && <> US Code current through {publicLawName(current)}.</>}
          </p>
          {data.ai_generated && (
            <p role="note" className="rounded-md border border-border bg-muted/50 px-3 py-2 text-xs text-muted-foreground">
              Explanations are AI-generated from the bill text and the current US Code and not reviewed by a
              person. They may contain errors; the bill&apos;s instruction and the current text are shown so
              you can check them.{" "}
              <Link href="/methodology#law-changes" className="underline underline-offset-2 hover:text-foreground">
                How explanations are made
              </Link>
            </p>
          )}
          <ul className="space-y-3">
            {data.changes.map((entry) => (
              <LawChangeItem key={entry.section_id} entry={entry} />
            ))}
          </ul>
          {data.explained && (
            <p className="text-xs text-muted-foreground">
              Explained by <em>{data.explained.model_used}</em> on{" "}
              {formatDate(data.explained.generated_at, "long")}
              {explainedAt && explainedAt !== current && <>, against the US Code through {publicLawName(explainedAt)}</>}
              .
            </p>
          )}
      </CollapsibleCard>
    </section>
  );
}

function LawChangeItem({ entry }: { entry: LawChangeEntry }) {
  const citation = lawCitation(entry);
  const title = entry.in_us_code ? entry.title_number : null;
  const section = entry.in_us_code ? entry.section_number : null;

  return (
    <li className="rounded-lg border border-border p-3 space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="outline" className="text-xs">
          {LAW_CHANGE_KIND_LABELS[entry.change_kind] ?? entry.change_kind}
        </Badge>
        <h4 className="font-mono text-sm font-semibold text-foreground">{citation}</h4>
        {!entry.in_us_code && <span className="text-xs text-muted-foreground">Not in the US Code</span>}
        {entry.in_us_code && entry.is_note && (
          <span className="text-xs text-muted-foreground">Statutory note printed with the section</span>
        )}
      </div>
      {entry.heading && <p className="text-sm font-medium text-foreground">{entry.heading}</p>}
      {entry.subsection_path && (
        <p className="text-xs text-muted-foreground">Subsections {entry.subsection_path}</p>
      )}

      {entry.in_us_code &&
        (entry.explanation ? (
          <p className="text-sm text-foreground leading-relaxed">{entry.explanation}</p>
        ) : (
          <p className="text-sm text-muted-foreground">No AI explanation of this change yet.</p>
        ))}

      {entry.instruction && (
        <div>
          <p className="text-xs font-semibold text-muted-foreground">The bill&apos;s instruction</p>
          <blockquote className="mt-1 border-l-2 border-border pl-3 text-sm text-muted-foreground whitespace-pre-wrap">
            {entry.instruction}
          </blockquote>
        </div>
      )}

      {title !== null && section !== null && (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
          {/* GET /law serves a section's text, never a note's (#573). */}
          {entry.loaded && !entry.is_note && (
            <LawSectionText title={title} section={section} citation={citation} />
          )}
          <a
            href={uscodeHouseUrl(title, section)}
            target="_blank"
            rel="noopener noreferrer"
            className="text-xs text-muted-foreground underline underline-offset-2 hover:text-foreground"
          >
            {entry.is_note ? `${title} U.S.C. ${section} and its notes` : citation} on uscode.house.gov
          </a>
        </div>
      )}

      {entry.also_changed_by.length > 0 && (
        <p className="text-xs text-muted-foreground">
          Also changed by{" "}
          {entry.also_changed_by.map((bill, i) => (
            <span key={bill.bill_id}>
              {i > 0 && ", "}
              <Link href={`/bills/${bill.bill_id}`} className="font-semibold tabular-nums text-foreground underline underline-offset-2">
                {billLabel(bill.bill_type, bill.number)}
              </Link>
            </span>
          ))}
        </p>
      )}
    </li>
  );
}
