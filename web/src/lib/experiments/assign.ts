// Which arm a request gets (docs/design/580-ab-experiments.md, options 1B and 2A). src/proxy.ts calls
// experimentResponse while an experiment runs; assignVariant is the pure part, tested without Next.
// Control requests pass through untouched, and treatment requests are rewritten to a static sibling
// route (`/vote` → `/vote/v/treatment`), so both arms stay static or ISR.

import { NextResponse, userAgent, type NextRequest } from "next/server";
import {
  COOKIE_NAME,
  dayStart,
  EXPERIMENTS,
  experimentForPath,
  experimentsNow,
  isLive,
  treatmentPathname,
  VARIANTS,
  type Experiment,
  type Variant,
} from "./registry";

/** The `Set-Cookie` that keeps a visitor in their arm. */
export interface ArmCookie {
  name: string;
  value: string;
  httpOnly: true;
  secure: true;
  sameSite: "lax";
  path: "/";
  /** Seconds until the experiment ends. */
  maxAge: number;
}

/** What the proxy does with one request. */
export interface Assignment {
  /** The experiment on this page, if any. */
  experiment?: Experiment;
  variant: Variant;
  /** Set only when a new arm was drawn. */
  cookie?: ArmCookie;
  /** The pathname to rewrite to, for treatment. */
  rewrite?: string;
}

/** What assignVariant needs from its surroundings; tests replace them. */
export interface AssignOptions {
  /** A uniform random number in [0, 1). Defaults to crypto.getRandomValues. */
  random?: () => number;
  /** Vercel's environment: `?variant=` is honored everywhere but "production". */
  vercelEnv?: string;
}

function cryptoRandom(): number {
  return crypto.getRandomValues(new Uint32Array(1))[0] / 2 ** 32;
}

/** The arm a `jab_exp` cookie holds for `experiment`, if it holds one for it. */
export function cookieVariant(value: string | undefined, experiment: Experiment): Variant | undefined {
  if (!value) return undefined;
  const dot = value.lastIndexOf(".");
  if (value.slice(0, dot) !== experiment.id) return undefined;
  return VARIANTS.find((v) => v === value.slice(dot + 1));
}

function isVariant(value: string | null): value is Variant {
  return VARIANTS.some((v) => v === value);
}

/** Crawlers and browsers sending Global Privacy Control get control, no cookie, and aren't counted. */
function excluded(request: NextRequest): boolean {
  return request.headers.get("sec-gpc") === "1" || userAgent(request).isBot;
}

function withRewrite(assignment: Assignment, pathname: string): Assignment {
  return assignment.variant === "treatment" ? { ...assignment, rewrite: treatmentPathname(pathname) } : assignment;
}

/**
 * Decides the arm for a request to `now`'s experiments. Outside its window, for crawlers and for
 * GPC browsers it's control with no cookie. A cookie naming this experiment keeps its arm;
 * otherwise an arm is drawn at random and set in a cookie that expires at the experiment's end.
 * Outside production, `?variant=control|treatment` forces an arm (no cookie), so both arms can be
 * reviewed on a preview before the experiment starts.
 */
export function assignVariant(
  request: NextRequest,
  experiments: readonly Experiment[],
  now: Date,
  opts: AssignOptions = {}
): Assignment {
  const { pathname, searchParams } = request.nextUrl;
  const experiment = experimentForPath(pathname, experiments);
  if (!experiment) return { variant: "control" };

  const forced = searchParams.get("variant");
  const vercelEnv = opts.vercelEnv ?? process.env.VERCEL_ENV;
  if (vercelEnv !== "production" && isVariant(forced)) {
    return withRewrite({ experiment, variant: forced }, pathname);
  }
  if (excluded(request) || !isLive(experiment, now)) return { experiment, variant: "control" };

  const kept = cookieVariant(request.cookies.get(COOKIE_NAME)?.value, experiment);
  if (kept) return withRewrite({ experiment, variant: kept }, pathname);

  const variant: Variant = (opts.random ?? cryptoRandom)() < 0.5 ? "control" : "treatment";
  const maxAge = Math.max(1, Math.ceil((dayStart(experiment.end) - now.getTime()) / 1000));
  const cookie: ArmCookie = {
    name: COOKIE_NAME,
    value: `${experiment.id}.${variant}`,
    httpOnly: true,
    secure: true,
    sameSite: "lax",
    path: "/",
    maxAge,
  };
  return withRewrite({ experiment, variant, cookie }, pathname);
}

/**
 * The proxy's response for a request: `next()` for control, a rewrite to the treatment route for
 * treatment, with the arm's cookie when one was drawn. src/proxy.ts is this and a constant matcher.
 */
export function experimentResponse(
  request: NextRequest,
  experiments: readonly Experiment[] = EXPERIMENTS,
  now: Date = experimentsNow(),
  opts: AssignOptions = {}
): NextResponse {
  const assignment = assignVariant(request, experiments, now, opts);
  let response: NextResponse;
  if (assignment.rewrite) {
    const url = request.nextUrl.clone();
    url.pathname = assignment.rewrite;
    response = NextResponse.rewrite(url);
  } else {
    response = NextResponse.next();
  }
  if (assignment.cookie) {
    const { name, value, ...options } = assignment.cookie;
    response.cookies.set(name, value, options);
  }
  return response;
}
