// The visitor's state, district and members of Congress, kept in localStorage (jab.reps.v1) so the
// scorecard doesn't look them up again. The address used for the lookup is never stored: saveReps
// copies only the fields below. `election` (#375) is kept only when the address votes in a
// different district at the next general election; entries saved before it have none.

import { createLocalStore, type LocalEnv, type LocalStore, type Parsed } from "./storage";

export const REPS_KEY = "jab.reps.v1";
export const REPS_CORRUPT_KEY = "jab.reps.corrupt";

export interface LocalRep {
  id: string;
  name: string;
  party: string;
  chamber: string;
  state: string;
  district?: number | null;
}

export interface LocalSeat {
  state: string;
  district: number;
}

/** The districts an address votes in at the next general election, and the ones it's in today. */
export interface LocalElection {
  congress: number;
  /** YYYY-MM-DD. */
  election_date: string;
  districts: LocalSeat[];
  current: LocalSeat[];
}

export interface LocalReps {
  state: string;
  district: number | null;
  looked_up_at: string;
  members: LocalRep[];
  election?: LocalElection;
}

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === "object" && x !== null && !Array.isArray(x);
}

function str(x: unknown): x is string {
  return typeof x === "string" && x.length > 0;
}

function district(x: unknown): number | null | undefined {
  if (x === null) return null;
  return typeof x === "number" && Number.isInteger(x) && x >= 0 ? x : undefined;
}

function member(x: unknown): LocalRep | null {
  if (!isRecord(x) || !str(x.id) || !str(x.name) || !str(x.chamber) || !str(x.state)) return null;
  const rep: LocalRep = {
    id: x.id,
    name: x.name,
    party: typeof x.party === "string" ? x.party : "",
    chamber: x.chamber,
    state: x.state,
  };
  const d = district(x.district);
  if (d !== undefined) rep.district = d;
  return rep;
}

function seats(x: unknown): LocalSeat[] | null {
  if (!Array.isArray(x)) return null;
  const out: LocalSeat[] = [];
  for (const s of x) {
    if (!isRecord(s) || !str(s.state)) return null;
    const d = district(s.district);
    if (d === undefined || d === null) return null;
    out.push({ state: s.state, district: d });
  }
  return out;
}

/** The election block, or null when it's missing or malformed (the line is then just not shown). */
function election(x: unknown): LocalElection | null {
  if (!isRecord(x) || typeof x.congress !== "number" || !Number.isInteger(x.congress)) return null;
  if (typeof x.election_date !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(x.election_date)) return null;
  const districts = seats(x.districts);
  const current = seats(x.current);
  if (!districts || districts.length === 0 || !current) return null;
  return { congress: x.congress, election_date: x.election_date, districts, current };
}

/** Copies the known fields only, so nothing else (an address, say) can be stored. */
export function cleanReps(x: unknown): LocalReps | null {
  if (!isRecord(x) || !str(x.state) || typeof x.looked_up_at !== "string" || !Array.isArray(x.members)) {
    return null;
  }
  const d = district(x.district);
  const members = x.members.map(member);
  if (d === undefined || members.some((m) => m === null)) return null;
  const reps: LocalReps = {
    state: x.state,
    district: d,
    looked_up_at: x.looked_up_at,
    members: members as LocalRep[],
  };
  const e = election(x.election);
  if (e) reps.election = e;
  return reps;
}

function parseReps(data: unknown): Parsed<LocalReps | null> {
  // clearReps stores null, so the value is empty rather than corrupt.
  if (data === null) return { value: null, lossy: false };
  if (!isRecord(data) || data.v !== 1) return null;
  const reps = cleanReps(data);
  return reps ? { value: reps, lossy: false } : null;
}

export interface LocalRepsStore extends Pick<LocalStore<LocalReps | null>, "subscribe" | "getSnapshot" | "getServerSnapshot"> {
  saveReps(reps: LocalReps): void;
  clearReps(): void;
}

export function createRepsStore(opts: { env?: () => LocalEnv | null } = {}): LocalRepsStore {
  const store = createLocalStore<LocalReps | null>({
    key: REPS_KEY,
    corruptKey: REPS_CORRUPT_KEY,
    empty: null,
    parse: parseReps,
    serialize: (reps) => (reps ? { v: 1, ...reps } : null),
    env: opts.env,
  });
  return {
    subscribe: store.subscribe,
    getSnapshot: store.getSnapshot,
    getServerSnapshot: store.getServerSnapshot,
    saveReps(reps) {
      const clean = cleanReps(reps);
      if (!clean) throw new Error("saveReps: invalid reps");
      store.update(() => clean);
    },
    clearReps() {
      store.update(() => null);
    },
  };
}
