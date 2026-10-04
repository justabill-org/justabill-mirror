import Link from "next/link";
import type { Amendment } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { congressGovAmendmentUrl } from "@/lib/trust";
import { formatDate } from "@/lib/utils";

interface AmendmentsListProps {
  amendments: Amendment[];
}

const AMENDMENT_TYPE_LABELS: Record<string, string> = {
  samdt: "S.Amdt.",
  hamdt: "H.Amdt.",
};

export function AmendmentsList({ amendments }: AmendmentsListProps) {
  if (amendments.length === 0) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="text-lg">Amendments</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            No amendments have been proposed for this bill.
          </p>
        </CardContent>
      </Card>
    );
  }

  // Sort by amendment number descending (newest first)
  const sortedAmendments = [...amendments].sort(
    (a, b) => b.amendment_number - a.amendment_number
  );

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-lg">Amendments</CardTitle>
      </CardHeader>
      <CardContent>
        <div className="space-y-3">
          {sortedAmendments.map((amendment) => (
            <AmendmentCard key={amendment.id} amendment={amendment} />
          ))}
        </div>
      </CardContent>
    </Card>
  );
}

function AmendmentCard({ amendment }: { amendment: Amendment }) {
  const typeLabel =
    AMENDMENT_TYPE_LABELS[amendment.amendment_type.toLowerCase()] ||
    amendment.amendment_type.toUpperCase();
  const amendmentId = `${typeLabel} ${amendment.amendment_number}`;
  const congressGovUrl = congressGovAmendmentUrl(amendment);

  const submittedDate = amendment.submitted_date
    ? formatDate(amendment.submitted_date)
    : null;

  const latestActionDate = amendment.latest_action?.actionDate
    ? formatDate(amendment.latest_action.actionDate)
    : null;

  return (
    <div className="rounded-lg border border-border p-4">
      <div className="flex flex-wrap items-start justify-between gap-3 mb-2">
        <div className="flex items-center gap-2">
          {congressGovUrl ? (
            <a
              href={congressGovUrl}
              target="_blank"
              rel="noopener noreferrer"
              aria-label={`${amendmentId} on Congress.gov (opens in a new tab)`}
              className="inline-flex min-h-6 items-center text-sm font-semibold tabular-nums text-link hover:underline"
            >
              {amendmentId}
            </a>
          ) : (
            <span className="text-sm font-semibold tabular-nums text-foreground">
              {amendmentId}
            </span>
          )}
          <Badge variant="outline">{amendment.chamber}</Badge>
        </div>
        {submittedDate && (
          <span className="text-xs text-muted-foreground">
            Submitted {submittedDate}
          </span>
        )}
      </div>

      {/* Description or Purpose */}
      {(amendment.description || amendment.purpose) && (
        <p className="text-sm text-foreground mb-2">
          {amendment.description || amendment.purpose}
        </p>
      )}

      {/* Sponsor */}
      {amendment.sponsor_id && (
        <div className="mb-2">
          <Link
            href={`/members/${amendment.sponsor_id}`}
            className="text-sm text-link hover:underline"
          >
            View Sponsor
          </Link>
        </div>
      )}

      {/* Latest Action */}
      {amendment.latest_action && (
        <div className="rounded-md bg-muted/50 p-3 mt-3">
          <p className="text-xs font-medium text-muted-foreground mb-1">
            Latest Action{latestActionDate && ` - ${latestActionDate}`}
          </p>
          <p className="text-sm text-foreground">
            {amendment.latest_action.text}
          </p>
        </div>
      )}
    </div>
  );
}
