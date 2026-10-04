import type { ReactNode } from "react";

import { Badge } from "@/components/ui/badge";

/**
 * Sample data for UI prototypes (docs/design/565-prototype-review-loop.md). A prototype shows data
 * the API doesn't serve yet through these helpers, on a private preview only. ESLint bans
 * importing this module (`no-restricted-imports` in eslint.config.mjs), so a prototype can't merge
 * until every use is replaced with real API data.
 */

/** What the badge says, visibly and as its accessible name. */
export const SAMPLE_DATA_LABEL = "Sample data, not from Congress.gov";

/** Marks a value as invented for a prototype. It returns the value unchanged. */
export function sample<T>(value: T): T {
  return value;
}

/** Wraps invented content with a visible "Sample data" badge, so nobody takes it for a fact. */
export function SampleData({ children }: { children: ReactNode }) {
  return (
    <span data-sample-data="" className="inline-flex flex-wrap items-center gap-1 rounded-md border border-dashed border-border p-1">
      <Badge variant="outline" role="note" aria-label={SAMPLE_DATA_LABEL} className="border-dashed">
        {SAMPLE_DATA_LABEL}
      </Badge>
      {children}
    </span>
  );
}
