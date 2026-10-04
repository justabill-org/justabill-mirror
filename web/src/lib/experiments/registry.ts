// The live A/B experiments (docs/design/580-ab-experiments.md). An experiment is code: its start PR
// adds an entry here, the treatment route and src/proxy.ts; its end PR removes all three. This
// file, src/proxy.ts and the endpoint are CODEOWNERS paths, so no experiment starts without the
// maintainer's review. registry.test.ts enforces the rules below on every entry.
//
// What may be tested: layout, navigation, readability and onboarding. Never bill facts, summaries,
// titles, vote labels or wording, the order or selection of bills, members or parties, share cards,
// aggregates, the trust pages or sign-in. Both arms show the same facts in the same order and pass
// the civic-neutrality review. A goal is an action ("casts a vote"), never a direction ("votes yea").

import { sampleSizePerArm } from "./stats";

/** The two arms of every experiment. */
export const VARIANTS = ["control", "treatment"] as const;

/** An arm of an experiment. */
export type Variant = (typeof VARIANTS)[number];

/** One A/B experiment. */
export interface Experiment {
  /** Lowercase words joined by hyphens, e.g. "vote-deck-layout". It goes into the cookie and the metrics. */
  id: string;
  /** The issue that states the hypothesis and records the readout. */
  issue: number;
  /** The control page's route, e.g. "/vote" or "/bills/[id]". The treatment is `<path>/v/treatment`. */
  path: string;
  /** What we expect the treatment to change, and why. */
  hypothesis: string;
  /** The one action counted as a conversion, e.g. "casts a first vote on /vote". */
  goal: string;
  /** The control's expected conversion rate, from Web Analytics or the A/A run (0.1 is 10%). */
  baseline: number;
  /** The smallest treatment rate worth detecting. */
  target: number;
  /** Visitors needed in each arm: at least sampleSizePerArm(baseline, target). */
  samplePerArm: number;
  /** The first day (UTC, YYYY-MM-DD) visitors are assigned. */
  start: string;
  /** The day (UTC, YYYY-MM-DD) assignment stops: everyone gets control from 00:00 UTC. */
  end: string;
  /** Set only in a PR the maintainer approves: lets the window overlap election week. */
  electionWeekApproved?: boolean;
}

/** The A/A run on /vote (#695): both arms are the same page. Its end PR removes this with its entry. */
export const VOTE_AA = "vote-aa";

/** The experiments in this deployment. Empty when nothing is being tested, and then there's no src/proxy.ts. */
export const EXPERIMENTS: readonly Experiment[] = [
  {
    id: VOTE_AA,
    issue: 695,
    path: "/vote",
    hypothesis:
      "Both arms are the same page, so about half of visitors land in each and their rates don't differ. " +
      "It checks the assignment, the counting and the readout, and measures /vote's baseline.",
    goal: "casts a first vote on /vote",
    // A guess until this run measures it; the readout reports the real rate for later experiments.
    baseline: 0.1,
    target: 0.12,
    samplePerArm: 3841,
    start: "2026-10-12",
    end: "2026-10-26",
  },
];

/** The cookie that holds the visitor's arm, as `<experiment id>.<variant>`. */
export const COOKIE_NAME = "jab_exp";

/** The longest an experiment (and so its cookie) may run. */
export const MAX_DAYS = 30;

/** Election week, 2026-11-02 to 11-06, as a half-open UTC interval. */
export const ELECTION_WEEK = { start: "2026-11-02", end: "2026-11-07" } as const;

const DAY_MS = 24 * 60 * 60 * 1000;
const ID_PATTERN = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
const DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/;
const PATH_PATTERN = /^\/(?:(?:[a-z0-9-]+|\[[a-zA-Z]+\])(?:\/(?:[a-z0-9-]+|\[[a-zA-Z]+\]))*)?$/;

/** The UTC midnight a YYYY-MM-DD date starts at, in milliseconds, or NaN if it isn't one. */
export function dayStart(date: string): number {
  if (!DATE_PATTERN.test(date)) return Number.NaN;
  const ms = Date.parse(`${date}T00:00:00Z`);
  return !Number.isNaN(ms) && new Date(ms).toISOString().startsWith(date) ? ms : Number.NaN;
}

/** Whether `now` falls in the experiment's window [start, end). */
export function isLive(experiment: Experiment, now: Date): boolean {
  const t = now.getTime();
  return t >= dayStart(experiment.start) && t < dayStart(experiment.end);
}

/**
 * The time the server judges experiments at: now, or outside production `JAB_EXPERIMENTS_NOW` (an
 * ISO time). The e2e tests set it to a moment the live experiment runs, so the proxy assigns arms
 * the same way whatever day the tests run (playwright.config.ts).
 */
export function experimentsNow(env: Record<string, string | undefined> = process.env): Date {
  const pinned = Date.parse(env.JAB_EXPERIMENTS_NOW ?? "");
  return env.VERCEL_ENV !== "production" && !Number.isNaN(pinned) ? new Date(pinned) : new Date();
}

/** The experiment with this id, if any. */
export function findExperiment(id: string, experiments: readonly Experiment[] = EXPERIMENTS): Experiment | undefined {
  return experiments.find((e) => e.id === id);
}

function pathRegExp(path: string): RegExp {
  const source = path
    .split("/")
    .map((segment) => (/^\[[a-zA-Z]+\]$/.test(segment) ? "[^/]+" : segment))
    .join("/");
  return new RegExp(`^${source}/?$`);
}

/** The experiment whose control page `pathname` is, if any. */
export function experimentForPath(
  pathname: string,
  experiments: readonly Experiment[] = EXPERIMENTS
): Experiment | undefined {
  return experiments.find((e) => pathRegExp(e.path).test(pathname));
}

/** The treatment route for a control page's concrete path: `/vote` → `/vote/v/treatment`. */
export function treatmentPathname(pathname: string): string {
  return `${pathname.replace(/\/$/, "")}/v/treatment`;
}

/** The proxy matcher for an experiment's path: `/bills/[id]` → `/bills/:id`. */
export function matcherFor(path: string): string {
  return path.replace(/\[([a-zA-Z]+)\]/g, ":$1");
}

/** Everything wrong with one entry, as sentences (empty when it's fine). */
function entryProblems(e: Experiment): string[] {
  const problems: string[] = [];
  const start = dayStart(e.start);
  const end = dayStart(e.end);
  if (!ID_PATTERN.test(e.id)) problems.push("its id must be lowercase words joined by hyphens");
  if (!Number.isInteger(e.issue) || e.issue <= 0) problems.push("it must name its issue");
  if (!PATH_PATTERN.test(e.path) || e.path.includes("/v/")) {
    problems.push(`its path ${JSON.stringify(e.path)} isn't a page route`);
  }
  if (!e.hypothesis.trim() || !e.goal.trim()) problems.push("it needs a hypothesis and a goal");
  if (Number.isNaN(start) || Number.isNaN(end)) {
    problems.push("start and end must be YYYY-MM-DD dates");
  } else {
    const days = (end - start) / DAY_MS;
    if (days <= 0) problems.push("it must end after it starts");
    if (days > MAX_DAYS) problems.push(`it runs ${days} days, more than ${MAX_DAYS}`);
    if (days % 7 !== 0) problems.push(`it runs ${days} days, not whole weeks`);
    const overlapsElection = start < dayStart(ELECTION_WEEK.end) && end > dayStart(ELECTION_WEEK.start);
    if (overlapsElection && !e.electionWeekApproved) {
      problems.push("it overlaps election week (2026-11-02 to 11-06) without electionWeekApproved");
    }
  }
  let needed = Number.NaN;
  try {
    needed = sampleSizePerArm(e.baseline, e.target);
  } catch (err) {
    problems.push((err as Error).message);
  }
  if (!Number.isNaN(needed) && !(e.samplePerArm >= needed)) {
    problems.push(`samplePerArm ${e.samplePerArm} is below the ${needed} that ${e.baseline} → ${e.target} needs`);
  }
  return problems;
}

/**
 * Everything wrong with a registry, as `<id>: <problem>` lines: the rules of design 580 (whole
 * weeks, at most 30 days, out of election week unless approved, a big enough sample, one
 * experiment per page, one at a time). registry.test.ts fails on any.
 */
export function registryProblems(experiments: readonly Experiment[]): string[] {
  const problems = experiments.flatMap((e) => entryProblems(e).map((p) => `${e.id}: ${p}`));
  experiments.forEach((e, i) => {
    for (const other of experiments.slice(0, i)) {
      if (other.id === e.id) problems.push(`${e.id}: two experiments share this id`);
      if (other.path === e.path) problems.push(`${e.id}: ${other.id} already tests ${e.path}`);
      // One cookie holds one experiment's arm, so a second live experiment would reassign visitors.
      const overlap = dayStart(e.start) < dayStart(other.end) && dayStart(other.start) < dayStart(e.end);
      if (overlap) problems.push(`${e.id}: its window overlaps ${other.id}'s (one experiment at a time)`);
    }
  });
  return problems;
}
