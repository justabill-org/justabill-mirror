import { describe, expect, it, vi } from "vitest";
import type { LocalVote, LocalVotes } from "../local/votes";
import { cleanReps } from "../local/reps";
import {
  billCongress,
  congressesVotedIn,
  votesByCongress,
  isMemberPhotoUrl,
  loadPositions,
  loadRepProfiles,
  lookUpReps,
  lookUpRepsAt,
  repProfile,
  repsFromLookup,
  score,
} from "../scorecard";
import type { MemberDetail, MemberPosition, RepsResponse } from "../types";

const at = "2026-10-04T15:00:00.000Z";
const v = (vote: LocalVote["vote"], title?: string): LocalVote => (title ? { vote, at, title } : { vote, at });

function pos(bill_id: string, vote: string, vote_date = "2025-05-01T12:00:00Z"): MemberPosition {
  return { bill_id, vote, vote_id: `house-119-${bill_id}`, vote_date, question: "On Passage" };
}

describe("score", () => {
  const cases: {
    name: string;
    votes: LocalVotes;
    positions: MemberPosition[];
    want: { compared: number; matching: number; member_absent: number; alignment_pct: number | null; rows: string[] };
  }[] = [
    {
      name: "counts matches over bills both voted yea or nay on",
      votes: { "hr-119-1": v("yea"), "hr-119-2": v("nay"), "hr-119-3": v("yea") },
      positions: [pos("hr-119-1", "yea"), pos("hr-119-2", "yea"), pos("hr-119-3", "yea")],
      want: { compared: 3, matching: 2, member_absent: 0, alignment_pct: 67, rows: ["hr-119-1", "hr-119-2", "hr-119-3"] },
    },
    {
      name: "never scores skips",
      votes: { "hr-119-1": v("skip"), "hr-119-2": v("nay") },
      positions: [pos("hr-119-1", "yea"), pos("hr-119-2", "nay")],
      want: { compared: 1, matching: 1, member_absent: 0, alignment_pct: 100, rows: ["hr-119-2"] },
    },
    {
      name: "shows present and not voting but leaves them out of the percentage",
      votes: { "hr-119-1": v("yea"), "hr-119-2": v("nay"), "hr-119-3": v("yea") },
      positions: [pos("hr-119-1", "present"), pos("hr-119-2", "not_voting"), pos("hr-119-3", "nay")],
      want: { compared: 1, matching: 0, member_absent: 2, alignment_pct: 0, rows: ["hr-119-1", "hr-119-2", "hr-119-3"] },
    },
    {
      name: "has no percentage when nothing overlaps",
      votes: { "hr-119-9": v("yea") },
      positions: [pos("hr-119-1", "yea")],
      want: { compared: 0, matching: 0, member_absent: 0, alignment_pct: null, rows: [] },
    },
    {
      name: "has no percentage when the member was only absent",
      votes: { "hr-119-1": v("yea") },
      positions: [pos("hr-119-1", "not_voting")],
      want: { compared: 0, matching: 0, member_absent: 1, alignment_pct: null, rows: ["hr-119-1"] },
    },
    {
      name: "scores several congresses together",
      votes: { "hr-118-5": v("nay"), "s-119-7": v("yea") },
      positions: [pos("hr-118-5", "Nay"), pos("s-119-7", "yea")],
      want: { compared: 2, matching: 2, member_absent: 0, alignment_pct: 100, rows: ["hr-118-5", "s-119-7"] },
    },
  ];

  for (const c of cases) {
    it(c.name, () => {
      const got = score(c.votes, { M1: c.positions }).M1;
      expect({
        compared: got.compared,
        matching: got.matching,
        member_absent: got.member_absent,
        alignment_pct: got.alignment_pct,
        rows: got.rows.map((r) => r.bill_id).sort(),
      }).toEqual(c.want);
    });
  }

  it("lists rows newest first, with the stored title and whether they count", () => {
    const votes = { "hr-119-1": v("yea", "Old Act"), "hr-119-2": v("nay", "New Act"), "hr-119-3": v("yea") };
    const got = score(votes, {
      M1: [
        pos("hr-119-1", "yea", "2025-01-10T00:00:00Z"),
        pos("hr-119-2", "yea", "2025-06-10T00:00:00Z"),
        pos("hr-119-3", "present", "2025-03-10T00:00:00Z"),
      ],
    }).M1;
    expect(got.rows.map((r) => [r.bill_id, r.bill_title, r.user_vote, r.member_vote, r.counted, r.matches])).toEqual([
      ["hr-119-2", "New Act", "nay", "yea", true, false],
      ["hr-119-3", "hr-119-3", "yea", "present", false, false],
      ["hr-119-1", "Old Act", "yea", "yea", true, true],
    ]);
    expect(got.rows[0]).toMatchObject({ vote_id: "house-119-hr-119-2", question: "On Passage" });
  });

  it("labels each row with its congress and the chamber the member voted in (#243)", () => {
    const votes = { "hr-118-5": v("nay"), "s-119-7": v("yea") };
    const got = score(votes, {
      M1: [{ ...pos("hr-118-5", "nay"), chamber: "House" }, pos("s-119-7", "yea", "2025-06-10T00:00:00Z")],
    }).M1;
    expect(got.rows.map((r) => [r.bill_id, r.congress, r.chamber])).toEqual([
      ["s-119-7", 119, undefined],
      ["hr-118-5", 118, "House"],
    ]);
  });

  it("scores each member separately", () => {
    const got = score({ "hr-119-1": v("yea") }, { A: [pos("hr-119-1", "yea")], B: [pos("hr-119-1", "nay")], C: [] });
    expect([got.A.alignment_pct, got.B.alignment_pct, got.C.alignment_pct]).toEqual([100, 0, null]);
  });
});

describe("billCongress", () => {
  it("reads the congress from a bill ID", () => {
    expect([billCongress("hr-118-2670"), billCongress("sjres-119-1"), billCongress("not-a-bill")]).toEqual([
      118,
      119,
      undefined,
    ]);
  });
});

describe("congressesVotedIn", () => {
  it("reads the congress from each yea or nay bill ID, not skips", () => {
    expect(
      congressesVotedIn({ "hr-119-1": v("yea"), "s-118-2": v("nay"), "hr-117-3": v("skip"), "sres-119-4": v("nay") })
    ).toEqual([118, 119]);
    expect(congressesVotedIn({})).toEqual([]);
  });
});

describe("votesByCongress", () => {
  it("counts each congress's yea and nay votes, leaving out skips and congresses with only skips", () => {
    expect(
      votesByCongress({
        "hr-119-1": v("yea"),
        "s-119-2": v("nay"),
        "hr-118-3": v("yea"),
        "hr-118-4": v("skip"),
        "hr-117-5": v("skip"),
        "not-a-bill": v("yea"),
      })
    ).toEqual({ 119: 2, 118: 1 });
    expect(votesByCongress({})).toEqual({});
  });
});

describe("loadPositions", () => {
  it("fetches every member in every congress and merges the pages", async () => {
    const fetcher = vi.fn(async (memberId: string, congress: number) => ({
      member_id: memberId,
      congress,
      rule: "final-passage-v1",
      positions: [pos(`hr-${congress}-1`, "yea")],
    }));
    const got = await loadPositions([{ id: "A" }, { id: "B" }], [118, 119], fetcher);
    expect(fetcher.mock.calls.sort()).toEqual([
      ["A", 118],
      ["A", 119],
      ["B", 118],
      ["B", 119],
    ]);
    expect(got.A.map((p) => p.bill_id)).toEqual(["hr-118-1", "hr-119-1"]);
  });

  it("fails the load when one request fails", async () => {
    const fetcher = vi.fn(async () => {
      throw new Error("503");
    });
    await expect(loadPositions([{ id: "A" }], [119], fetcher)).rejects.toThrow("503");
  });
});

const lookup: RepsResponse = {
  reps: [{ bioguide_id: "H000001", first_name: "Hana", last_name: "Hill" }],
  senators: [
    { bioguide_id: "S000001", first_name: "Sam", last_name: "Stone" },
    { bioguide_id: "S000002", first_name: "Sue", last_name: "Sand" },
  ],
  districts: [{ state: "CO", district: 3, source: "geocoder" }],
};

describe("repsFromLookup", () => {
  it("keeps the state, district and members, and drops the address", () => {
    const got = repsFromLookup(lookup, new Date(at));
    expect(got).toEqual({
      state: "CO",
      district: 3,
      looked_up_at: at,
      members: [
        { id: "H000001", name: "Hana Hill", party: "", chamber: "House", state: "CO", district: 3 },
        { id: "S000001", name: "Sam Stone", party: "", chamber: "Senate", state: "CO" },
        { id: "S000002", name: "Sue Sand", party: "", chamber: "Senate", state: "CO" },
      ],
    });
    expect(JSON.stringify(got)).not.toContain("Pennsylvania");
    expect(cleanReps(got)).toEqual(got);
  });

  it("leaves the district out when the address matched several", () => {
    const got = repsFromLookup(
      { ...lookup, districts: [{ state: "CO", district: 3 }, { state: "CO", district: 4 }] },
      new Date(at)
    );
    expect(got?.district).toBeNull();
    expect(got?.members[0]).not.toHaveProperty("district");
  });

  it("keeps the next election's districts only when they changed (#375)", () => {
    const election = {
      congress: 120,
      election_date: "2026-11-03",
      districts: [{ state: "CO", district: 8, at_large: false, congress: 120, source: "geocoder" }],
      changed: true,
    };
    const got = repsFromLookup({ ...lookup, election }, new Date(at));
    expect(got?.election).toEqual({
      congress: 120,
      election_date: "2026-11-03",
      districts: [{ state: "CO", district: 8 }],
      current: [{ state: "CO", district: 3 }],
    });
    expect(cleanReps(got)).toEqual(got);
    expect(repsFromLookup({ ...lookup, election: { ...election, changed: false } }, new Date(at))).not.toHaveProperty(
      "election"
    );
  });

  it("returns null when no district was found", () => {
    expect(repsFromLookup({ ...lookup, districts: [] }, new Date(at))).toBeNull();
  });
});

describe("lookUpReps", () => {
  it("sends the trimmed address to the lookup once", async () => {
    const fn = vi.fn(async () => lookup);
    const got = await lookUpReps("  1 Main St  ", new Date(at), fn);
    expect(fn).toHaveBeenCalledExactlyOnceWith("1 Main St");
    expect(got?.members).toHaveLength(3);
  });
});

describe("lookUpRepsAt", () => {
  it("sends the point to the lookup once and keeps only what's safe to store", async () => {
    const fn = vi.fn(async () => lookup);
    const got = await lookUpRepsAt({ lat: 41.14, lon: -104.82 }, new Date(at), fn);
    expect(fn).toHaveBeenCalledExactlyOnceWith(41.14, -104.82);
    expect(got?.members).toHaveLength(3);
    expect(JSON.stringify(got)).not.toContain("41.14");
  });
});

// #667: the rep cards' party and photo, from GET /members/{id}.
describe("rep profiles", () => {
  const photo = "https://www.congress.gov/img/member/s001150_200.jpg";
  const term = (congress: number, chamber: string, party: string) => ({
    member_id: "S001150",
    congress,
    chamber,
    state: "CA",
    party,
  });
  const detail = (overrides: Partial<MemberDetail> = {}): MemberDetail => ({
    bioguide_id: "S001150",
    first_name: "Adam",
    last_name: "Schiff",
    photo_url: photo,
    terms: [term(118, "House", "D"), term(119, "Senate", "D")],
    recent_votes: [],
    ...overrides,
  });

  it.each([
    [photo, true],
    ["https://www.congress.gov/img/member/s001150.jpg", true],
    [undefined, false],
    ["", false],
    ["not a url", false],
    ["http://www.congress.gov/img/member/s001150_200.jpg", false],
    ["https://congress.gov/img/member/s001150_200.jpg", false],
    ["https://www.congress.gov/img/other/s001150_200.jpg", false],
    ["https://www.congress.gov/img/member/s001150_200.jpg?x=1", false],
    ["https://example.com/img/member/s001150_200.jpg", false],
  ])("isMemberPhotoUrl(%j) is %s", (url, want) => {
    expect(isMemberPhotoUrl(url)).toBe(want);
  });

  it("takes the party of the latest term in the rep's chamber, and the photo", () => {
    const d = detail({ terms: [term(117, "House", "R"), term(118, "House", "D"), term(119, "Senate", "I")] });
    expect(repProfile(d, { chamber: "House" })).toEqual({ party: "D", photoUrl: photo });
    expect(repProfile(d, { chamber: "Senate" })).toEqual({ party: "I", photoUrl: photo });
  });

  it("leaves out a party no term gives and a photo from anywhere but Congress.gov", () => {
    expect(repProfile(detail({ terms: [] }), { chamber: "House" })).toEqual({ party: "", photoUrl: photo });
    expect(repProfile(detail({ photo_url: "https://example.com/a.jpg" }), { chamber: "Senate" })).toEqual({
      party: "D",
    });
    expect(repProfile(detail({ photo_url: undefined }), { chamber: "Senate" })).toEqual({ party: "D" });
  });

  it("loads each rep's profile and leaves out the ones that fail", async () => {
    const fetchMember = vi.fn(async (id: string) => {
      if (id === "B2") throw new Error("503");
      return detail({ bioguide_id: id });
    });
    const got = await loadRepProfiles(
      [
        { id: "A1", chamber: "Senate" },
        { id: "B2", chamber: "Senate" },
      ],
      fetchMember,
    );
    expect(got).toEqual({ A1: { party: "D", photoUrl: photo } });
    expect(fetchMember.mock.calls.map(([id]) => id)).toEqual(["A1", "B2"]);
  });
});
