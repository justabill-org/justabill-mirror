import { isValidElement, type ReactElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BILL_VIEWS } from "@/lib/bill-views";
import { billsBecameLaw } from "@/lib/examples";

// /bills counts a filtered list against the visitor's own rate limit, and keeps each view's first
// page cached and shared (#607, docs/design/607-per-visitor-web-limits.md; views #666).

const mockFetch = vi.fn();
vi.stubGlobal("fetch", mockFetch);
// Tests resolve `#server-key` without the react-server condition: load the server components' version.
vi.mock("#server-key", () => import("@/lib/server-key"));
vi.mock("next/headers", () => ({ headers: async () => new Headers({ "x-real-ip": "203.0.113.7" }) }));
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
}));

const { default: BillsPage } = await import("@/app/(app)/bills/page");

afterEach(() => {
  mockFetch.mockReset();
  vi.unstubAllEnvs();
});

function elements(node: ReactNode): ReactElement<Record<string, unknown>>[] {
  if (Array.isArray(node)) return node.flatMap(elements);
  if (!isValidElement<Record<string, unknown>>(node)) return [];
  return [node, ...elements(node.props.children as ReactNode)];
}

/** The list's server component, as the page renders it for these search parameters. */
async function grid(search: Record<string, string>) {
  const tree = elements(await BillsPage({ searchParams: Promise.resolve(search) }));
  const found = tree.find((el) => typeof el.props.visitor === "boolean");
  if (!found) throw new Error("no bill list in the page");
  return found;
}

/** Renders the list's server component and returns the API call it made. */
async function listCall(search: Record<string, string>): Promise<[string, RequestInit]> {
  mockFetch.mockResolvedValue({
    ok: true,
    status: 200,
    json: () => Promise.resolve({ items: [], total: 0, offset: 0, limit: 12 }),
  });
  const el = await grid(search);
  mockFetch.mockClear(); // the page's own read of the congresses, for the default congress (#717)
  await (el.type as (props: Record<string, unknown>) => Promise<ReactNode>)(el.props);
  return mockFetch.mock.calls[0] as [string, RequestInit];
}

describe("BillsPage per-visitor lists", () => {
  it.each([
    ["no parameters", {}],
    ["the default page spelled out", { offset: "0", limit: "12" }],
    ["unvoted only, which the browser fetches itself", { unvoted: "true" }],
    ["an empty search box", { q: "" }],
    ["another view (#666)", { show: "passed" }],
    ["the other sort", { sort: "introduced_date" }],
    ["a view and a sort", { show: "committee", sort: "introduced_date" }],
    ["the old status parameter, which /bills no longer reads", { status: "became_law" }],
    ["a bill type the API doesn't have, which is dropped (#796)", { type: "zz" }],
    ["a chamber that isn't one, which is dropped (#796)", { chamber: "both" }],
  ])("keeps a view's first page shared: %s", async (_, search) => {
    expect((await grid(search)).props.visitor).toBe(false);
  });

  it.each([
    ["a search", { q: "tax" }],
    ["a congress", { congress: "118" }],
    ["a bill type", { type: "s" }],
    ["a chamber", { chamber: "senate" }],
    ["a policy area (#796)", { area: "Health" }],
    ["a search in another view", { show: "all", q: "tax" }],
    ["a later page", { offset: "12" }],
    ["another page size", { limit: "24" }],
  ])("counts the visitor for %s", async (_, search) => {
    expect((await grid(search)).props.visitor).toBe(true);
  });

  it("sends a filtered list's call with the visitor's IP on Vercel, uncached", async () => {
    vi.stubEnv("VERCEL", "1");
    const [url, init] = await listCall({ q: "tax" });
    expect(url).toContain("q=tax");
    expect(init.cache).toBe("no-store");
    expect(init.headers).toHaveProperty("X-Visitor-IP", "203.0.113.7");
  });

  it("caches the default list and sends no IP, even on Vercel", async () => {
    vi.stubEnv("VERCEL", "1");
    const [, init] = await listCall({});
    expect(init.next).toMatchObject({ revalidate: 300 });
    expect(init.headers).not.toHaveProperty("X-Visitor-IP");
  });

  it("sends no IP off Vercel", async () => {
    vi.stubEnv("VERCEL", "");
    const [, init] = await listCall({ q: "tax" });
    expect(init.headers).not.toHaveProperty("X-Visitor-IP");
    expect(init.next).toMatchObject({ revalidate: 300 });
  });

  it("sends a view of several statuses as one list call, with the visitor's IP on Vercel (#712)", async () => {
    vi.stubEnv("VERCEL", "1");
    const [url, init] = await listCall({ show: "passed", q: "tax" });
    expect(mockFetch.mock.calls).toHaveLength(1);
    expect(url).toContain("status=passed_house%2Cpassed_senate%2Cresolving_differences%2Cto_president%2Cvetoed");
    expect(init.headers).toHaveProperty("X-Visitor-IP", "203.0.113.7");
  });

  it("pages a view of several statuses past its old cap of 100 bills", async () => {
    const [url] = await listCall({ show: "laws", offset: "120" });
    expect(url).toContain("offset=120");
    expect(url).toContain("status=became_law%2Csigned");
  });
});

describe("BillsPage default congress (#717)", () => {
  const json = (body: unknown) => ({ ok: true, status: 200, json: () => Promise.resolve(body) });
  const withCongresses = () =>
    mockFetch.mockImplementation((url: string) =>
      Promise.resolve(
        String(url).includes("/congresses")
          ? json([{ number: 118 }, { number: 119, is_current: true }])
          : json({ items: [], total: 0, offset: 0, limit: 12 }),
      ),
    );
  const params = async (search: Record<string, string>) =>
    (await grid(search)).props as { params: { congress?: number }; visitor: boolean };

  it("lists the current congress when the URL names none, as a shared list", async () => {
    withCongresses();
    expect(await params({})).toMatchObject({ params: { congress: 119 }, visitor: false });
    expect(await params({ congress: "119" })).toMatchObject({ params: { congress: 119 }, visitor: false });
    expect(await params({ congress: "x" })).toMatchObject({ params: { congress: 119 }, visitor: false });
  });

  it("lists another congress, or every one for ?congress=all, counted against the visitor", async () => {
    withCongresses();
    expect(await params({ congress: "118" })).toMatchObject({ params: { congress: 118 }, visitor: true });
    const all = await params({ congress: "all" });
    expect(all.params.congress).toBeUndefined();
    expect(all.visitor).toBe(true);
  });

  it("lists every congress when the congresses can't be read", async () => {
    mockFetch.mockRejectedValue(new Error("down"));
    const { params: p, visitor } = await params({});
    expect(p.congress).toBeUndefined();
    expect(visitor).toBe(false);
  });
});

/** The view controls' server component, as the page renders it. */
async function controls(search: Record<string, string>) {
  const tree = elements(await BillsPage({ searchParams: Promise.resolve(search) }));
  const found = tree.find((el) => el.props.base !== undefined);
  if (!found) throw new Error("no controls in the page");
  return found;
}

describe("BillsPage view counts", () => {
  const counts = { by_status: { became_law: 5, signed: 1, passed_house: 10, vetoed: 5, in_committee: 3 }, total: 30 };
  const ok = (body: unknown) => ({ ok: true, status: 200, json: () => Promise.resolve(body) });
  async function renderCounts(search: Record<string, string>) {
    const el = await controls(search);
    mockFetch.mockClear(); // the page's own read of the congresses
    const rendered = (await (el.type as (p: Record<string, unknown>) => Promise<ReactElement<Record<string, unknown>>>)(
      el.props,
    )) as ReactElement<{ counts: Record<string, number> }>;
    return rendered.props.counts;
  }

  it("counts every view from one cached, shared GET /bills/counts, even with filters (#713)", async () => {
    vi.stubEnv("VERCEL", "1");
    mockFetch.mockImplementation(async (url: string) => ok(String(url).includes("/counts") ? counts : []));
    const got = await renderCounts({ congress: "119", type: "hr", show: "passed", sort: "introduced_date" });

    const calls = mockFetch.mock.calls.filter(([url]) => String(url).includes("/bills"));
    expect(calls).toHaveLength(1);
    const [url, init] = calls[0] as [string, RequestInit];
    expect(url).toMatch(/\/api\/v1\/bills\/counts\?congress=119&type=hr$/);
    expect(init.headers).not.toHaveProperty("X-Visitor-IP");
    expect(init.next).toMatchObject({ revalidate: 300 });
    expect(got).toEqual({ laws: 6, passed: 15, committee: 3, all: 30 });
  });

  it("counts during a search too, against the visitor like the search's list", async () => {
    vi.stubEnv("VERCEL", "1");
    mockFetch.mockImplementation(async (url: string) => ok(String(url).includes("/counts") ? counts : []));
    const got = await renderCounts({ q: "tax" });
    const calls = mockFetch.mock.calls.filter(([url]) => String(url).includes("/bills"));
    expect(calls).toHaveLength(1);
    const [url, init] = calls[0] as [string, RequestInit];
    expect(url).toContain("/bills/counts?");
    expect(url).toContain("q=tax");
    expect(init.headers).toHaveProperty("X-Visitor-IP", "203.0.113.7");
    expect(got).toEqual({ laws: 6, passed: 15, committee: 3, all: 30 });
  });

  it("shows no counts when they can't be read", async () => {
    vi.stubEnv("NODE_ENV", "production");
    mockFetch.mockImplementation(async (url: string) =>
      String(url).includes("/counts") ? { ok: false, status: 500, statusText: "boom", text: async () => "" } : ok([]),
    );
    expect(await renderCounts({})).toEqual({});
  });
});

describe("BillsPage list", () => {
  it("lists the Laws view by default in one call, by latest action, with AI and CRS summaries", async () => {
    const [a, b] = billsBecameLaw.items;
    mockFetch.mockImplementation(async () => ({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ items: [b, a].map((x) => ({ ...x, summary: null })), total: 41, offset: 0, limit: 12 }),
    }));
    const el = await grid({});
    mockFetch.mockClear(); // the page's own read of the congresses
    const html = renderToStaticMarkup(
      (await (el.type as (props: Record<string, unknown>) => Promise<ReactElement>)(el.props)) as ReactElement,
    );
    const urls = mockFetch.mock.calls.map(([url]) => String(url));
    expect(urls).toHaveLength(1);
    expect(urls[0]).toContain("status=became_law%2Csigned");
    expect(urls[0]).toContain("sort=latest_action");
    expect(urls[0]).toContain("include=summary,crs_summary");
    expect(html).toContain('aria-label="Laws: bills"');
    expect(html).toContain("Laws: showing 1-2 of 41 bills");
    // The API's order is kept.
    expect(html.indexOf(`/bills/${b.id}`)).toBeLessThan(html.indexOf(`/bills/${a.id}`));
  });

  it("says when a view has no bills", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ items: [], total: 0, offset: 0, limit: 12 }),
    });
    const el = await grid({ show: "committee", q: "zzz" });
    const html = renderToStaticMarkup(
      (await (el.type as (props: Record<string, unknown>) => Promise<ReactElement>)(el.props)) as ReactElement,
    );
    expect(html).toContain("No bills found");
    expect(html).toContain("Try another view, or a different search.");
  });
});

/** Renders a server component the page holds and returns its output. */
async function run(el: ReactElement<Record<string, unknown>>): Promise<ReactElement<Record<string, unknown>>> {
  return (await (el.type as (p: Record<string, unknown>) => Promise<ReactElement>)(el.props)) as ReactElement<
    Record<string, unknown>
  >;
}

describe("BillsPage policy area and shared filters (#796)", () => {
  const json = (body: unknown) => ({ ok: true, status: 200, json: () => Promise.resolve(body) });
  const law = { ...billsBecameLaw.items[0], summary: null };
  /** One bill at every status a view lists, in a total of one (as each list's single law). */
  const oneOfEach = {
    by_status: Object.fromEntries(BILL_VIEWS.flatMap((v) => v.statuses).map((s) => [s, 1])),
    total: 1,
  };
  /** The API with the 119th current, two policy areas, and one law in every list. */
  const api = (areas: () => Promise<unknown> = async () => json({ policy_areas: ["Health", "Taxation"] })) =>
    mockFetch.mockImplementation((url: string) => {
      const u = String(url);
      if (u.includes("/congresses")) return Promise.resolve(json([{ number: 119, is_current: true }]));
      if (u.includes("/policy-areas")) return areas();
      if (u.includes("/bills/counts")) return Promise.resolve(json(oneOfEach));
      return Promise.resolve(json({ items: [law], total: 1, offset: 0, limit: 12 }));
    });
  const listUrls = () =>
    mockFetch.mock.calls.map(([url]) => String(url)).filter((u) => u.includes("/bills?") && u.includes("include="));

  it("filters the list by policy area", async () => {
    api();
    const el = await grid({ area: "Health", show: "committee" });
    mockFetch.mockClear();
    await run(el);
    expect(listUrls()).toHaveLength(1);
    expect(listUrls()[0]).toContain("policy_area=Health");
    expect(listUrls()[0]).toContain("status=in_committee");
  });

  it("counts each view under the policy area", async () => {
    api();
    const el = await controls({ area: "Taxation" });
    mockFetch.mockClear();
    await run(el);
    const counts = mockFetch.mock.calls.map(([url]) => String(url)).filter((u) => u.includes("/bills/counts"));
    expect(counts).toHaveLength(1);
    expect(counts[0]).toContain("policy_area=Taxation");
  });

  it("hands the controls every policy area", async () => {
    api();
    const rendered = await run(await controls({}));
    expect(rendered.props.policyAreas).toEqual(["Health", "Taxation"]);
  });

  it("leaves the policy areas out when they can't be read, and still counts the views", async () => {
    api(async () => ({ ok: false, status: 503, json: () => Promise.resolve({ error: "down" }) }));
    const rendered = await run(await controls({}));
    expect(rendered.props.policyAreas).toBeNull();
    expect(rendered.props.counts).toEqual({ laws: 2, passed: 5, committee: 1, all: 1 });
  });

  it("reads a hand-edited URL as the defaults, sending the API nothing it would refuse (Review Focus 3)", async () => {
    api();
    const el = await grid({ show: "xyz", type: "zz", area: "Nope", congress: "abc", sort: "1", chamber: "both" });
    mockFetch.mockClear();
    await run(el);
    // Laws, the default view: one list of both its statuses (#712).
    expect(listUrls()).toHaveLength(1);
    for (const url of listUrls()) {
      const q = new URL(url).searchParams;
      expect(q.getAll("status").join(",")).toBe("became_law,signed");
      expect(q.get("congress")).toBe("119");
      expect(q.get("sort")).toBe("latest_action");
      expect(q.get("policy_area")).toBe("Nope");
      expect(q.has("type")).toBe(false);
      expect(q.has("chamber")).toBe(false);
    }
  });

  it("cuts a policy area to the 100 characters the API takes", async () => {
    api();
    const el = await grid({ area: "x".repeat(250) });
    mockFetch.mockClear();
    await run(el);
    for (const url of listUrls()) expect(new URL(url).searchParams.get("policy_area")).toBe("x".repeat(100));
  });

  it("links the list to /vote with the same filters, leaving out the page size and Unvoted only", async () => {
    api();
    const el = await grid({ show: "passed", type: "hr", area: "Health", limit: "24", unvoted: "true" });
    const html = renderToStaticMarkup(await run(el));
    expect(html).toContain('href="/vote?show=passed&amp;type=hr&amp;area=Health"');
    expect(html).toContain(">Vote on these</a>");
  });

  it("links the default list to /vote as it is", async () => {
    api();
    const html = renderToStaticMarkup(await run(await grid({})));
    expect(html).toContain('href="/vote"');
  });
});
