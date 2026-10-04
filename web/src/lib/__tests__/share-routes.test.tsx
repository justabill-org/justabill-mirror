import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type {
  AggregateCell,
  BillAggregatesResponse,
  BillDetailResponse,
  MemberDetail,
  MemberPositionsResponse,
} from "../types";

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  getBill: vi.fn(),
  getBillAggregates: vi.fn(),
  getMember: vi.fn(),
  getMemberPositions: vi.fn(),
}));

const api = await import("../api");
const { ApiError } = api;
const { loadAggregateCard, loadBillCard } = await import("../share-data");
const aggregateImage = await import("@/app/(public)/share/aggregate/[bill]/[scope]/image.png/route");
const aggregatePage = await import("@/app/(public)/share/aggregate/[bill]/[scope]/page");
const billImage = await import("@/app/(public)/share/bill/[bill]/[vote]/image.png/route");
const billMemberImage = await import("@/app/(public)/share/bill/[bill]/[vote]/[member]/image.png/route");
const billMemberPage = await import("@/app/(public)/share/bill/[bill]/[vote]/[member]/page");
const billPage = await import("@/app/(public)/share/bill/[bill]/[vote]/page");
const { default: MethodologyPage } = await import("@/app/(public)/(trust)/methodology/page");
const { ShareCardImage } = await import("@/components/share/card");
const { loadLogo } = await import("@/components/share/image");
const { aggregateCardCopy, billCardCopy } = await import("../share");

const senator: MemberDetail = {
  bioguide_id: "X000001",
  first_name: "Jane",
  last_name: "Doe",
  terms: [
    { member_id: "X000001", congress: 118, chamber: "Senate", state: "NY", party: "D" },
    { member_id: "X000001", congress: 119, chamber: "Senate", state: "NY", party: "D" },
  ],
  recent_votes: [],
};

function positions(congress: number, votes: string[], billPrefix = `hr-${congress}`): MemberPositionsResponse {
  return {
    member_id: "X000001",
    congress,
    rule: "final-passage-v1",
    positions: votes.map((vote, i) => ({
      bill_id: `${billPrefix}-${i + 1}`,
      vote,
      vote_id: `v${i}`,
      vote_date: "2025-05-22T14:03:00Z",
    })),
  };
}

const bill = {
  bill: { id: "hr-119-1", congress: 119, bill_type: "hr", number: 1, title: "Example Act of 2025" },
} as unknown as BillDetailResponse;

const notFound = () => new ApiError(404, "Not Found", "{}");

/** Stands in for loadLogo()'s data URI where a test renders the card markup. */
const LOGO = "data:image/png;base64,bG9nbw==";

function aggCell(scopeKey: string, overrides: Partial<AggregateCell> = {}): AggregateCell {
  return {
    scope: scopeKey === "" ? "national" : scopeKey.includes("-") ? "district" : "state",
    scope_key: scopeKey,
    status: "published",
    yea_pct: 62,
    nay_pct: 38,
    voters_floor: 340,
    published_at: "2026-10-01T12:00:00Z",
    ...overrides,
  };
}

const aggregates: BillAggregatesResponse = {
  bill_id: "hr-119-1",
  as_of: "2026-10-01T12:00:00Z",
  national: aggCell(""),
  states: [aggCell("CA", { yea_pct: 55, nay_pct: 45 })],
  districts: [aggCell("CA-12", { status: "held" }), aggCell("TX-2", { yea_pct: null, nay_pct: null })],
};

beforeEach(() => {
  vi.mocked(api.getMember).mockReset().mockResolvedValue(senator);
  vi.mocked(api.getBill).mockReset().mockResolvedValue(bill);
  vi.mocked(api.getBillAggregates).mockReset().mockResolvedValue(aggregates);
  // The senator voted Yea on hr-119-1.
  vi.mocked(api.getMemberPositions).mockReset().mockImplementation(async (_id, congress) =>
    positions(congress, ["yea", "nay", "present", "not_voting"])
  );
});

const billMemberParams = (bill: string, vote: string, member: string) => ({
  params: Promise.resolve({ bill, vote, member }),
});
const req = new Request("http://localhost/share");

describe("loadBillCard", () => {
  it("looks the member's vote up from their positions in the bill's congress", async () => {
    const card = await loadBillCard({ billId: "hr-119-1", vote: "nay", memberId: "X000001" });
    expect(card?.copy.summary).toBe("I'd vote Nay on H.R. 1");
    expect(card?.copy.plain).toContain("Sen. Jane Doe (D-NY) voted Yea");
    expect(card?.actionHref).toBe("/bills/hr-119-1");
    expect(api.getMemberPositions).toHaveBeenCalledWith("X000001", 119);
  });

  it("says so when the member has no recorded position", async () => {
    vi.mocked(api.getMemberPositions).mockResolvedValue(positions(119, []));
    const card = await loadBillCard({ billId: "hr-119-1", vote: "yea", memberId: "X000001" });
    expect(card?.copy.plain).toContain("has no recorded vote on this bill");
  });

  it("needs no member data for a vote-only card", async () => {
    const card = await loadBillCard({ billId: "hr-119-1", vote: "yea" });
    expect(card?.copy.plain).toBe("H.R. 1 · 119th Congress. Example Act of 2025. I'd vote Yea.");
    expect(api.getMember).not.toHaveBeenCalled();
    expect(api.getMemberPositions).not.toHaveBeenCalled();
  });

  it("refuses an unknown bill or member, and positions it can't load", async () => {
    vi.mocked(api.getBill).mockRejectedValueOnce(notFound());
    expect(await loadBillCard({ billId: "hr-119-9", vote: "yea" })).toBeNull();
    vi.mocked(api.getMember).mockRejectedValueOnce(notFound());
    expect(await loadBillCard({ billId: "hr-119-1", vote: "yea", memberId: "X000009" })).toBeNull();
    vi.mocked(api.getMemberPositions).mockRejectedValueOnce(notFound());
    expect(await loadBillCard({ billId: "hr-119-1", vote: "yea", memberId: "X000001" })).toBeNull();
  });
});

describe("image routes", () => {
  it("renders a 1200×630 PNG with CDN-only cache headers", async () => {
    const res = await billMemberImage.GET(req, billMemberParams("hr-119-1", "yea", "X000001"));
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("image/png");
    expect(res.headers.get("cache-control")).toBe(
      "public, max-age=3600, s-maxage=86400, stale-while-revalidate=604800"
    );
    const png = Buffer.from(await res.arrayBuffer());
    expect(png.subarray(1, 4).toString()).toBe("PNG");
    expect(png.readUInt32BE(16)).toBe(1200);
    expect(png.readUInt32BE(20)).toBe(630);
  });

  it.each([
    ["hr-119-1", "present", "X000001"], // not yea or nay
    ["hr-119-1", "yea", "x000001"], // not a bioguide ID
    ["hr-119-1", "yea", "X000009"], // unknown member
  ])("404s %s/%s/%s with a short cache", async (bill, vote, member) => {
    vi.mocked(api.getMember).mockImplementation(async (id) => {
      if (id !== senator.bioguide_id) throw notFound();
      return senator;
    });
    const res = await billMemberImage.GET(req, billMemberParams(bill, vote, member));
    expect(res.status).toBe(404);
    expect(res.headers.get("cache-control")).toBe("public, max-age=300, s-maxage=300");
  });

  it("404s a non-canonical bill path without calling the API", async () => {
    const res = await billImage.GET(req, { params: Promise.resolve({ bill: "HR-119-1", vote: "yea" }) });
    expect(res.status).toBe(404);
    expect(api.getBill).not.toHaveBeenCalled();
  });

  it("renders a vote-only bill card", async () => {
    const res = await billImage.GET(req, { params: Promise.resolve({ bill: "hr-119-1", vote: "nay" }) });
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("image/png");
  });
});

describe("share pages", () => {
  it("sets Open Graph and X metadata with the image and noindex", async () => {
    const meta = await billMemberPage.generateMetadata({
      params: Promise.resolve({ bill: "hr-119-1", vote: "yea", member: "X000001" }),
    });
    expect(meta.title).toBe("I'd vote Yea on H.R. 1 | Just a Bill");
    expect(meta.robots).toEqual({ index: false, follow: true });
    expect(meta.openGraph?.url).toBe("/share/bill/hr-119-1/yea/X000001");
    expect(meta.openGraph?.images).toEqual([
      {
        url: "/share/bill/hr-119-1/yea/X000001/image.png",
        width: 1200,
        height: 630,
        alt: "H.R. 1 · 119th Congress. Example Act of 2025. I'd vote Yea. Sen. Jane Doe (D-NY) voted Yea.",
      },
    ]);
    expect(meta.twitter).toMatchObject({ card: "summary_large_image" });
  });

  it("renders the card as text with one call to action", async () => {
    const html = renderToStaticMarkup(await billMemberPage.default(billMemberParams("hr-119-1", "yea", "X000001")));
    expect(html).toContain("<strong class=\"font-bold\">Sen. Jane Doe (D-NY)</strong>");
    expect(html).toMatch(/<a [^>]*href="\/bills\/hr-119-1"[^>]*>How would you vote\?<\/a>/);
    expect(html).toContain('href="/members/X000001"');
  });

  it("links to how votes are compared on the Methodology page (#165)", async () => {
    const html = renderToStaticMarkup(await billMemberPage.default(billMemberParams("hr-119-1", "yea", "X000001")));
    expect(html).toMatch(/<a [^>]*href="\/methodology#scorecard"[^>]*>How votes are compared<\/a>/);
    expect(renderToStaticMarkup(<MethodologyPage />)).toContain('id="scorecard"');
  });

  it("404s a member the API doesn't have", async () => {
    vi.mocked(api.getMember).mockRejectedValue(notFound());
    const notFoundError = { digest: "NEXT_HTTP_ERROR_FALLBACK;404" };
    const params = () => billMemberParams("hr-119-1", "yea", "X000009");
    await expect(billMemberPage.generateMetadata(params())).rejects.toMatchObject(notFoundError);
    await expect(billMemberPage.default(params())).rejects.toMatchObject(notFoundError);
  });
});

describe("bill share page (#872)", () => {
  const billParams = (bill: string, vote: string) => ({ params: Promise.resolve({ bill, vote }) });
  const notFoundError = { digest: "NEXT_HTTP_ERROR_FALLBACK;404" };

  it.each(["yea", "nay"])("renders the %s card as text, linking to the bill to vote on it", async (vote) => {
    const label = vote === "yea" ? "Yea" : "Nay";
    const html = renderToStaticMarkup(await billPage.default(billParams("hr-119-1", vote)));
    expect(html).toMatch(new RegExp(`<h1 [^>]*><span>I&#x27;d vote </span><strong class="font-bold">${label}</strong></h1>`));
    expect(html).toContain("Example Act of 2025");
    expect(html).toMatch(/<a [^>]*href="\/bills\/hr-119-1"[^>]*>How would you vote\?<\/a>/);
    expect(api.getBill).toHaveBeenCalledWith("hr-119-1");
  });

  it("sets the page's preview to the card, noindex", async () => {
    const meta = await billPage.generateMetadata(billParams("hr-119-1", "nay"));
    expect(meta.title).toBe("I'd vote Nay on H.R. 1 | Just a Bill");
    expect(meta.robots).toEqual({ index: false, follow: true });
    expect(meta.openGraph?.url).toBe("/share/bill/hr-119-1/nay");
    expect(meta.openGraph?.images).toEqual([
      {
        url: "/share/bill/hr-119-1/nay/image.png",
        width: 1200,
        height: 630,
        alt: "H.R. 1 · 119th Congress. Example Act of 2025. I'd vote Nay.",
      },
    ]);
  });

  it.each([
    ["a vote that isn't yea or nay", "hr-119-1", "present"],
    ["a vote in another case", "hr-119-1", "Yea"],
    ["a malformed bill", "HR-119-1", "yea"],
  ])("404s %s without calling the API", async (_name, bill, vote) => {
    await expect(billPage.default(billParams(bill, vote))).rejects.toMatchObject(notFoundError);
    await expect(billPage.generateMetadata(billParams(bill, vote))).rejects.toMatchObject(notFoundError);
    expect(api.getBill).not.toHaveBeenCalled();
  });

  it("404s a bill the API doesn't have", async () => {
    vi.mocked(api.getBill).mockRejectedValue(notFound());
    await expect(billPage.default(billParams("hr-119-9", "yea"))).rejects.toMatchObject(notFoundError);
    await expect(billPage.generateMetadata(billParams("hr-119-9", "yea"))).rejects.toMatchObject(notFoundError);
  });

  it("throws other API errors rather than caching a 404 for a day", async () => {
    vi.mocked(api.getBill).mockRejectedValue(new ApiError(503, "Service Unavailable", "{}"));
    await expect(billPage.default(billParams("hr-119-1", "yea"))).rejects.toMatchObject({ status: 503 });
  });
});

describe("card image footer", () => {
  const copy = () =>
    billCardCopy({ billType: "hr", number: 1, congress: 119, title: "Example Act of 2025" }, "yea", {
      member: { firstName: "Jane", lastName: "Doe", chamber: "Senate", state: "NY", party: "D" },
      position: "nay",
    });

  it("points to the Methodology page on the site's domain", () => {
    const html = renderToStaticMarkup(<ShareCardImage copy={copy()} domain="justabill.io" logo={LOGO} />);
    expect(html).toContain("How votes are compared: justabill.io/methodology");
  });

  it("shows the mascot beside the name, not a JB tile (#706)", () => {
    const html = renderToStaticMarkup(<ShareCardImage copy={copy()} domain="justabill.io" logo={LOGO} />);
    expect(html).toMatch(new RegExp(`<img src="${LOGO}" alt="" width="44" height="44"[^>]*/?>.*Just a Bill`));
    expect(html).not.toContain(">JB<");
  });

  it("loads the mascot once, as a PNG data URI 88px square (#706)", async () => {
    const uri = await loadLogo();
    expect(uri).toMatch(/^data:image\/png;base64,/);
    const png = Buffer.from(uri.slice(uri.indexOf(",") + 1), "base64");
    expect(png.subarray(1, 4).toString()).toBe("PNG");
    expect([png.readUInt32BE(16), png.readUInt32BE(20)]).toEqual([88, 88]);
    expect(await loadLogo()).toBe(uri);
  });

  it("prints no footer link when the domain isn't known", () => {
    expect(renderToStaticMarkup(<ShareCardImage copy={copy()} logo={LOGO} />)).not.toContain("How votes are compared");
  });
});

const aggParams = (bill: string, scope: string) => ({ params: Promise.resolve({ bill, scope }) });

describe("loadAggregateCard (#166)", () => {
  it("prints the national and a state's published cell, from the API, never the URL", async () => {
    const national = await loadAggregateCard({ billId: "hr-119-1", scopeKey: "" });
    expect(national?.copy.summary).toBe("Just a Bill users nationwide: 62% Yea, 38% Nay on H.R. 1");
    expect(national?.actionHref).toBe("/bills/hr-119-1");
    expect(national?.methodology).toEqual({ href: "/methodology#aggregates", label: "How these numbers work" });
    const state = await loadAggregateCard({ billId: "hr-119-1", scopeKey: "CA" });
    expect(state?.copy.summary).toBe("Just a Bill users in CA: 55% Yea, 45% Nay on H.R. 1");
    expect(api.getBillAggregates).toHaveBeenCalledWith("hr-119-1");
  });

  it.each([
    ["CA-12", "held, under review"],
    ["TX-2", "no numbers"],
    ["NY-3", "suppressed, so not served"],
  ])("refuses %s (%s)", async (scopeKey) => {
    expect(await loadAggregateCard({ billId: "hr-119-1", scopeKey })).toBeNull();
  });

  it("refuses the national cell when it isn't served", async () => {
    vi.mocked(api.getBillAggregates).mockResolvedValue({ ...aggregates, national: null });
    expect(await loadAggregateCard({ billId: "hr-119-1", scopeKey: "" })).toBeNull();
  });

  it("refuses every card while aggregates are off, and an unknown bill", async () => {
    vi.mocked(api.getBillAggregates).mockRejectedValueOnce(notFound());
    expect(await loadAggregateCard({ billId: "hr-119-1", scopeKey: "" })).toBeNull();
    vi.mocked(api.getBill).mockRejectedValueOnce(notFound());
    expect(await loadAggregateCard({ billId: "hr-119-9", scopeKey: "" })).toBeNull();
  });

  it("propagates API errors other than 404/400", async () => {
    vi.mocked(api.getBillAggregates).mockRejectedValue(new ApiError(503, "Unavailable", ""));
    await expect(loadAggregateCard({ billId: "hr-119-1", scopeKey: "" })).rejects.toThrow("503");
  });
});

describe("aggregate card routes (#166)", () => {
  it("renders a PNG cached an hour at the edge", async () => {
    const res = await aggregateImage.GET(req, aggParams("hr-119-1", "national"));
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("image/png");
    expect(res.headers.get("cache-control")).toBe("public, max-age=600, s-maxage=3600, stale-while-revalidate=3600");
    const png = Buffer.from(await res.arrayBuffer());
    expect(png.readUInt32BE(16)).toBe(1200);
    expect(png.readUInt32BE(20)).toBe(630);
  });

  it.each([
    ["hr-119-1", "CA-12"], // held
    ["hr-119-1", "NY-3"], // not served
    ["hr-119-1", "ca"], // not canonical
    ["HR-119-1", "national"],
  ])("404s %s/%s with a short cache", async (bill, scope) => {
    const res = await aggregateImage.GET(req, aggParams(bill, scope));
    expect(res.status).toBe(404);
    expect(res.headers.get("cache-control")).toBe("public, max-age=300, s-maxage=300");
  });

  it("sets the card as the page's preview, noindex", async () => {
    const meta = await aggregatePage.generateMetadata(aggParams("hr-119-1", "CA"));
    expect(meta.title).toBe("Just a Bill users in CA: 55% Yea, 45% Nay on H.R. 1 | Just a Bill");
    expect(meta.robots).toEqual({ index: false, follow: true });
    expect(meta.openGraph?.url).toBe("/share/aggregate/hr-119-1/CA");
    expect(meta.openGraph?.images).toMatchObject([{ url: "/share/aggregate/hr-119-1/CA/image.png", width: 1200 }]);
  });

  it("renders the card as text with the label and the aggregates methodology", async () => {
    const html = renderToStaticMarkup(await aggregatePage.default(aggParams("hr-119-1", "national")));
    expect(html).toContain('<strong class="font-bold">62%\u00a0Yea, </strong>');
    expect(html).toContain("Just a Bill users, not a poll.");
    expect(html).toContain("can&#x27;t tell you what a district, a state or the country thinks");
    expect(html).toMatch(/<a [^>]*href="\/methodology#aggregates"[^>]*>How these numbers work<\/a>/);
    expect(html).toContain('href="/bills/hr-119-1"');
    expect(renderToStaticMarkup(<MethodologyPage />)).toContain('id="aggregates"');
  });

  it("404s a held cell's page", async () => {
    const notFoundError = { digest: "NEXT_HTTP_ERROR_FALLBACK;404" };
    await expect(aggregatePage.default(aggParams("hr-119-1", "CA-12"))).rejects.toMatchObject(notFoundError);
  });

  it("prints the label and the aggregates methodology on the image", () => {
    const copy = aggregateCardCopy(
      { billType: "hr", number: 1, congress: 119, title: "Example Act of 2025" },
      { ...aggCell(""), yea_pct: 62, nay_pct: 38 }
    );
    const html = renderToStaticMarkup(<ShareCardImage copy={copy} domain="justabill.io" logo={LOGO} />);
    expect(html).toContain("Just a Bill users, not a poll.");
    expect(html).toContain("How these numbers work: justabill.io/methodology#aggregates");
  });
});
