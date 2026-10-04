import Link from "next/link";
import type { GraphRelatedBill } from "@/lib/types";
import { BILL_STATUS_LABELS } from "@/lib/types";
import { billLabel, ordinal } from "@/lib/graph";
import { Badge } from "@/components/ui/badge";
import { CollapsibleCard } from "@/components/ui/collapsible-card";

interface RelatedBillsProps {
  bills: GraphRelatedBill[];
}

export function RelatedBills({ bills }: RelatedBillsProps) {
  if (bills.length === 0) return null;

  return (
    <section className="mt-8" aria-label="Related bills">
      <CollapsibleCard
        title="Related bills"
        summary={bills.length === 1 ? "1 related bill" : `${bills.length} related bills`}
        contentClassName="space-y-4"
      >
          <p className="text-sm text-muted-foreground">
            Bills Congress.gov lists as related, and bills that share legislative subjects with this one.
          </p>
          <ul className="space-y-2">
            {bills.map((related) => (
              <li key={related.bill_id}>
                <Link
                  href={`/bills/${related.bill_id}`}
                  className="flex items-start justify-between gap-3 rounded-lg border border-border p-3 hover:bg-muted transition-colors"
                >
                  <div className="min-w-0">
                    <span className="text-sm font-semibold tabular-nums text-foreground">
                      {billLabel(related.bill_type, related.number)}
                    </span>
                    <span className="ml-2 text-xs text-muted-foreground">{ordinal(related.congress)} Congress</span>
                    <p className="text-sm text-muted-foreground mt-0.5">{related.title}</p>
                  </div>
                  <div className="flex shrink-0 flex-col items-end gap-1">
                    {related.relation_types.map((type) => (
                      <Badge key={type} variant="outline" className="text-xs">
                        {type}
                      </Badge>
                    ))}
                    {related.shared_subjects > 0 && (
                      <span className="text-xs text-muted-foreground">
                        {related.shared_subjects} shared {related.shared_subjects === 1 ? "subject" : "subjects"}
                      </span>
                    )}
                    {related.current_status && (
                      <span className="text-xs text-muted-foreground">
                        {BILL_STATUS_LABELS[related.current_status] ?? related.current_status}
                      </span>
                    )}
                  </div>
                </Link>
              </li>
            ))}
          </ul>
      </CollapsibleCard>
    </section>
  );
}
