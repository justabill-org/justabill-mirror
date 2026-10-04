import type { BillStatus, BillStatusEntry, BillType } from "@/lib/types";
import { BILL_STATUS_LABELS } from "@/lib/types";
import { billProgress, type ProgressStep, type StepState } from "@/lib/bill-progress";
import { formatDate } from "@/lib/utils";

interface StatusProgressProps {
  currentStatus?: BillStatus;
  statusHistory: BillStatusEntry[];
}

interface BillProgressProps extends StatusProgressProps {
  billType: BillType;
  introducedDate?: string;
}

/** What each state says under the step, visibly or (for upcoming steps) to screen readers only. */
const STATE_TEXT: Record<StepState, string> = {
  reached: "",
  current: "",
  skipped: "Skipped",
  unrecorded: "Date not recorded",
  originated: "Began in committee",
  upcoming: "Not yet",
};

/**
 * The bill's steps with their dates (#664). Below the lg breakpoint it's a compact vertical list,
 * one line per step with the date on the right; from lg it's a horizontal timeline. The current
 * step is marked in text ("Current step") and with aria-current, not by color alone.
 */
export function StatusProgress({ billType, currentStatus, statusHistory, introducedDate }: BillProgressProps) {
  const steps = billProgress({ billType, currentStatus, statusHistory, introducedDate });
  const furthest = steps.reduce((max, s, i) => (s.state === "upcoming" ? max : i), -1);

  return (
    <ol className="flex flex-col lg:flex-row">
      {steps.map((step, i) => (
        <li
          key={step.status}
          aria-current={step.state === "current" ? "step" : undefined}
          className="relative flex items-start gap-3 pb-3 last:pb-0 lg:flex-1 lg:flex-col lg:items-center lg:gap-2 lg:pb-0 lg:text-center"
        >
          {i < steps.length - 1 && (
            <span
              aria-hidden="true"
              className={`absolute left-2.5 top-5 bottom-0 w-0.5 -translate-x-1/2 lg:left-1/2 lg:top-3 lg:bottom-auto lg:h-0.5 lg:w-full lg:translate-x-0 ${
                i < furthest ? "bg-foreground" : "bg-border"
              }`}
            />
          )}
          <StepDot state={step.state} />
          <div className="flex min-w-0 flex-1 flex-wrap items-baseline justify-between gap-x-3 gap-y-1 pt-px lg:block lg:flex-none lg:pt-0">
            <p
              className={`text-sm leading-5 lg:text-xs lg:leading-tight ${
                step.state === "current"
                  ? "font-semibold text-foreground"
                  : step.state === "upcoming"
                    ? "text-muted-foreground"
                    : "font-medium text-foreground"
              }`}
            >
              {BILL_STATUS_LABELS[step.status]}
            </p>
            <StepDetail step={step} />
            {step.state === "current" && (
              <span className="basis-full lg:mt-1 lg:block">
                <span className="inline-block rounded-full bg-primary px-2 py-0.5 text-[11px] font-semibold leading-4 text-primary-foreground">
                  Current step
                </span>
              </span>
            )}
          </div>
        </li>
      ))}
    </ol>
  );
}

function StepDetail({ step }: { step: ProgressStep }) {
  if (step.state === "upcoming") return <span className="sr-only">{STATE_TEXT.upcoming}</span>;
  return (
    <p className="shrink-0 text-xs leading-5 text-muted-foreground tabular-nums lg:mt-0.5 lg:leading-normal">
      {step.date ? (
        <time dateTime={step.date.slice(0, 10)}>{formatDate(step.date)}</time>
      ) : (
        STATE_TEXT[step.state]
      )}
    </p>
  );
}

function StepDot({ state }: { state: StepState }) {
  const base = "relative z-10 flex h-5 w-5 shrink-0 items-center justify-center rounded-full border-2 lg:h-6 lg:w-6";
  switch (state) {
    case "current":
      return (
        <span aria-hidden="true" className={`${base} border-foreground bg-foreground ring-[3px] ring-foreground/20 lg:ring-4`}>
          <span className="h-1.5 w-1.5 rounded-full bg-background lg:h-2 lg:w-2" />
        </span>
      );
    case "reached":
    case "originated":
      return (
        <span aria-hidden="true" className={`${base} border-foreground bg-foreground`}>
          <CheckIcon className="h-3 w-3 text-background" />
        </span>
      );
    case "unrecorded":
      return (
        <span aria-hidden="true" className={`${base} border-foreground bg-background`}>
          <CheckIcon className="h-3 w-3 text-foreground" />
        </span>
      );
    case "skipped":
      return (
        <span aria-hidden="true" className={`${base} border-dashed border-muted-foreground bg-background`}>
          <span className="h-0.5 w-2.5 bg-muted-foreground" />
        </span>
      );
    default:
      return <span aria-hidden="true" className={`${base} border-border bg-background`} />;
  }
}

function CheckIcon({ className }: { className: string }) {
  return (
    <svg className={className} fill="currentColor" viewBox="0 0 20 20">
      <path
        fillRule="evenodd"
        d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z"
        clipRule="evenodd"
      />
    </svg>
  );
}
