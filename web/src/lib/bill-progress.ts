import type { BillStatus, BillStatusEntry, BillType } from "./types";

/**
 * Where a step stands. `reached` and `current` have a date from the status history (Introduced
 * falls back to the bill's introduced date). A step the bill moved past without an entry is
 * `skipped` when bills can go without it (committee, a report, resolving differences, a
 * signature), or `unrecorded` when it can't have been skipped: the record is missing its date.
 * `originated` is In committee on a bill reported without a referral: a committee wrote it (an
 * original measure, #781), so there is no referral date to record.
 */
export type StepState = "reached" | "current" | "skipped" | "unrecorded" | "originated" | "upcoming";

export interface ProgressStep {
  status: BillStatus;
  state: StepState;
  /** The step's date (an API date, UTC midnight), when the record has one. */
  date?: string;
}

/** Steps a bill can move past without reaching. */
const SKIPPABLE = new Set<BillStatus>(["in_committee", "reported", "resolving_differences", "signed"]);

/**
 * The lifecycle for a bill type, in the order the bill goes through it: the chamber it started
 * in first. Simple resolutions stay in one chamber, concurrent resolutions skip the President, and
 * a vetoed bill shows the veto in place of the signature (it can still become law by override).
 */
export function lifecycle(billType: BillType, vetoed: boolean): BillStatus[] {
  const houseFirst = billType.startsWith("h");
  const committee: BillStatus[] = ["introduced", "in_committee", "reported"];
  const first: BillStatus = houseFirst ? "passed_house" : "passed_senate";
  const second: BillStatus = houseFirst ? "passed_senate" : "passed_house";
  if (billType === "hres" || billType === "sres") return [...committee, first];
  const congress: BillStatus[] = [...committee, first, second, "resolving_differences"];
  if (billType === "hconres" || billType === "sconres") return congress;
  return [...congress, "to_president", vetoed ? "vetoed" : "signed", "became_law"];
}

interface ProgressInput {
  billType: BillType;
  currentStatus?: BillStatus;
  statusHistory: BillStatusEntry[];
  introducedDate?: string;
}

/** Each step of the bill's lifecycle with its date and state. */
export function billProgress({ billType, currentStatus, statusHistory, introducedDate }: ProgressInput): ProgressStep[] {
  const dates = new Map<BillStatus, string>();
  for (const entry of statusHistory) {
    // The first time the bill reached a step is its date.
    const seen = dates.get(entry.status);
    if (!seen || entry.status_date < seen) dates.set(entry.status, entry.status_date);
  }
  if (!dates.has("introduced") && introducedDate) dates.set("introduced", introducedDate);

  const vetoed = dates.has("vetoed") || currentStatus === "vetoed";
  const steps = lifecycle(billType, vetoed);
  const isReached = (s: BillStatus) => dates.has(s) || s === currentStatus;
  const furthest = steps.reduce((max, s, i) => (isReached(s) ? i : max), -1);

  return steps.map((status, i) => {
    const date = dates.get(status);
    if (status === currentStatus) return { status, state: "current", date };
    if (date) return { status, state: "reached", date };
    if (i > furthest) return { status, state: "upcoming" };
    // Reported with no referral: the committee wrote the bill itself (an original measure).
    if (status === "in_committee" && isReached("reported")) return { status, state: "originated" };
    return { status, state: SKIPPABLE.has(status) ? "skipped" : "unrecorded" };
  });
}
