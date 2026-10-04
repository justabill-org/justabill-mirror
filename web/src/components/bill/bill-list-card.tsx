import Link from "next/link";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { PartyIndicator } from "@/components/member/party-indicator";
import { BillLinkShareButton } from "@/components/share/bill-link-share";
import { BILL_STEPS, billDescription, billStep, type BillSort } from "@/lib/bill-views";
import type { BillListItem } from "@/lib/types";
import { BILL_STATUS_LABELS, BILL_TYPE_LABELS } from "@/lib/types";
import { formatDate } from "@/lib/utils";

// A bill on /bills (#666): what it does in one line, how far it got, and who sponsored it.

interface BillListCardProps {
  bill: BillListItem;
  /** The list's sort, which picks the date the card shows. */
  sort: BillSort;
  /** The title's heading level: 2 under a page's h1 (/bills), 3 under a section's h2 (home). */
  headingLevel?: 2 | 3;
}

export function BillListCard({ bill, sort, headingLevel = 2 }: BillListCardProps) {
  const Heading = headingLevel === 3 ? "h3" : "h2";
  const typeLabel = BILL_TYPE_LABELS[bill.bill_type] || bill.bill_type.toUpperCase();
  const description = billDescription(bill);
  const sponsor = bill.sponsors?.[0];
  const step = billStep(bill.current_status);
  // A law's card says when it became law: the label stays visible on a phone, where the sort
  // can't name this date.
  const becameLaw = sort !== "introduced_date" && bill.current_status === "became_law" && bill.status_date;
  const [dateLabel, dateValue] = becameLaw
    ? ["Became law", bill.status_date]
    : sort === "introduced_date"
      ? ["Introduced", bill.introduced_date]
      : ["Latest action", bill.latest_action?.actionDate];

  // The card is a container, not one big link (#811): its title's link is stretched over the card,
  // so the rest of the card still opens the bill, and Share sits above it, outside the link.
  return (
    <Card className="group relative h-full transition-colors hover:border-foreground/40">
      <div className="flex h-full flex-col gap-2 p-4 sm:gap-3 sm:p-5">
        <div className="flex items-start justify-between gap-3">
          <span className="text-sm font-semibold tabular-nums text-foreground">
            {typeLabel} {bill.number}
          </span>
          <div className="flex items-center gap-1">
            {bill.current_status && (
              <Badge variant={step === BILL_STEPS.length ? "success" : "secondary"}>
                {BILL_STATUS_LABELS[bill.current_status] ?? bill.current_status}
              </Badge>
            )}
            <BillLinkShareButton
              billId={bill.id}
              title={bill.title}
              variant="ghost"
              className="relative z-10 -my-1.5 -mr-2 text-muted-foreground hover:text-foreground"
            />
          </div>
        </div>

        <Heading className="line-clamp-2 text-[15px] font-semibold sm:text-base leading-snug text-foreground underline-offset-4 group-hover:underline">
          <Link
            href={`/bills/${bill.id}`}
            className="after:absolute after:inset-0 after:rounded-lg focus-visible:outline-none focus-visible:after:outline-2 focus-visible:after:outline-offset-2 focus-visible:after:outline-ring"
          >
            {bill.title}
          </Link>
        </Heading>

        {description ? (
          <p className="line-clamp-2 text-sm leading-snug text-foreground/90 sm:line-clamp-3 sm:leading-relaxed">
            <span className="font-medium text-muted-foreground">{description.label}: </span>
            {description.text}
          </p>
        ) : (
          <p className="text-sm italic text-muted-foreground">No plain-language summary yet.</p>
        )}

        {/* A finished bill has no progress to show: its badge already says it all (#717). */}
        {step < BILL_STEPS.length && <BillSteps step={step} />}

        <div className="mt-auto flex items-center justify-between gap-3 text-xs text-muted-foreground sm:flex-wrap sm:gap-y-1 sm:pt-1 sm:text-sm">
          {sponsor && (
            <span className="flex min-w-0 items-center gap-1.5">
              <PartyIndicator party={sponsor.party} size="sm" />
              <span className="truncate">
                {sponsor.fullName.split("[")[0].trim()}
                {sponsor.party && sponsor.state && ` (${sponsor.party}-${sponsor.state})`}
              </span>
            </span>
          )}
          {/* On a phone the date stands alone: the sort under Filters says which date it is. */}
          {dateValue && (
            <span className="shrink-0 text-xs">
              <span className={becameLaw ? undefined : "sr-only sm:not-sr-only"}>{dateLabel} </span>
              {formatDate(dateValue)}
            </span>
          )}
        </div>
      </div>
    </Card>
  );
}

/**
 * Four segments, filled up to the step the bill reached, with the step named beside them (under
 * them from sm up) and for screen readers. Only for bills still on their way.
 */
function BillSteps({ step }: { step: number }) {
  return (
    <div className="flex items-center gap-3 sm:block">
      <div className="flex flex-1 items-center gap-1" aria-hidden="true">
        {BILL_STEPS.map((label, i) => (
          <span key={label} className={`h-1.5 flex-1 rounded-full ${i < step ? "bg-foreground" : "bg-muted"}`} />
        ))}
      </div>
      <p className="shrink-0 text-xs text-muted-foreground sm:mt-1">
        <span className="sr-only">Progress: step {step} of {BILL_STEPS.length}, </span>
        {BILL_STEPS[step - 1]}
      </p>
    </div>
  );
}

export function BillListCardSkeleton() {
  return (
    <Card className="h-full">
      <div className="flex flex-col gap-3 p-5">
        <div className="flex items-start justify-between">
          <div className="h-5 w-20 animate-pulse rounded bg-muted" />
          <div className="h-5 w-24 animate-pulse rounded-full bg-muted" />
        </div>
        <div className="h-10 w-full animate-pulse rounded bg-muted" />
        <div className="h-10 w-full animate-pulse rounded bg-muted" />
        <div className="h-1.5 w-full animate-pulse rounded bg-muted" />
        <div className="h-4 w-40 animate-pulse rounded bg-muted" />
      </div>
    </Card>
  );
}
