import type { BillSummary } from "@/lib/types";

interface SummaryProvenanceProps {
  summary: BillSummary;
  className?: string;
}

/**
 * The line saying which text version and model an AI summary comes from, e.g. "Based on the
 * Reported in House text · gemini-3.5-flash". It names the version by its Congress.gov name when
 * the bill still lists it, otherwise by its code. Older summaries have no source version, so they
 * show the model only; with neither, nothing renders.
 */
export function SummaryProvenance({ summary, className }: SummaryProvenanceProps) {
  const version = summary.source_version_name || summary.source_version_code;
  const model = summary.model_used;
  if (!version && !model) return null;

  return (
    <p className={className ?? "text-xs text-muted-foreground"}>
      {version ? (
        <>
          Based on the <em>{version}</em> text
          {model && (
            <>
              {" · "}
              <em>{model}</em>
            </>
          )}
        </>
      ) : (
        <>
          Model: <em>{model}</em>
        </>
      )}
    </p>
  );
}
