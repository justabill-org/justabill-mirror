import Link from "next/link";
import type { Bill } from "@/lib/types";
import { BILL_TYPE_LABELS, BILL_STATUS_LABELS } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { PartyIndicator } from "@/components/member/party-indicator";
import { formatDate } from "@/lib/utils";

interface BillCardProps {
  bill: Bill;
}

export function BillCard({ bill }: BillCardProps) {
  const typeLabel = BILL_TYPE_LABELS[bill.bill_type] || bill.bill_type.toUpperCase();

  const sponsor = bill.sponsors?.[0];
  const introducedDate = bill.introduced_date
    ? formatDate(bill.introduced_date)
    : null;

  return (
    <Link href={`/bills/${bill.id}`}>
      <Card className="group h-full cursor-pointer transition-colors hover:border-foreground/40">
        <div className="flex flex-col gap-3 p-5">
          {/* Header with bill number and status */}
          <div className="flex items-start justify-between gap-3">
            <span className="text-sm font-semibold tabular-nums text-foreground">
              {typeLabel} {bill.number}
            </span>
            <StatusBadge status={bill.current_status} />
          </div>

          {/* Title */}
          <h3 className="line-clamp-2 text-base font-semibold leading-snug text-foreground underline-offset-4 group-hover:underline">
            {bill.title}
          </h3>

          {/* Latest action preview */}
          {bill.latest_action && (
            <p className="line-clamp-2 text-sm text-muted-foreground leading-relaxed">
              {bill.latest_action.text}
            </p>
          )}

          {/* Policy area */}
          {bill.policy_area && (
            <Badge variant="secondary" className="w-fit">
              {bill.policy_area}
            </Badge>
          )}

          {/* Footer with sponsor and date */}
          <div className="mt-auto flex items-center justify-between gap-2 pt-2 text-sm text-muted-foreground">
            {sponsor && (
              <div className="flex items-center gap-1.5 truncate">
                <PartyIndicator party={sponsor.party} size="sm" />
                <span className="truncate">{sponsor.fullName.split("[")[0].trim()}</span>
              </div>
            )}
            {introducedDate && (
              <span className="shrink-0 text-xs">{introducedDate}</span>
            )}
          </div>
        </div>
      </Card>
    </Link>
  );
}

function StatusBadge({ status }: { status?: string }) {
  if (!status) return null;

  const label = BILL_STATUS_LABELS[status as keyof typeof BILL_STATUS_LABELS] || status;

  let variant: "default" | "secondary" | "success" | "outline" = "secondary";
  if (status === "became_law" || status === "signed") {
    variant = "success";
  } else if (status === "passed_house" || status === "passed_senate") {
    variant = "default";
  }

  return <Badge variant={variant}>{label}</Badge>;
}

export function BillCardSkeleton() {
  return (
    <Card className="h-full">
      <div className="flex flex-col gap-3 p-5">
        <div className="flex items-start justify-between">
          <div className="h-5 w-20 animate-pulse rounded bg-muted" />
          <div className="h-5 w-24 animate-pulse rounded-full bg-muted" />
        </div>
        <div className="h-12 w-full animate-pulse rounded bg-muted" />
        <div className="h-5 w-32 animate-pulse rounded-full bg-muted" />
        <div className="mt-auto flex items-center justify-between pt-2">
          <div className="h-4 w-40 animate-pulse rounded bg-muted" />
          <div className="h-4 w-20 animate-pulse rounded bg-muted" />
        </div>
      </div>
    </Card>
  );
}
