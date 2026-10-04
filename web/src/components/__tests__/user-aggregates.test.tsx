// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { fakeAuthStore } from "@/test/fake-auth";
import { axeViolations } from "@/test/axe";
import type { AggregateCell, BillAggregatesResponse, ScopeAggregateResponse } from "@/lib/types";

// #126: the bill page's "How Just a Bill users voted" panel. The API is mocked; the visitor's
// constituency comes from the signed-in account or the reps saved in this browser.

vi.mock("@/lib/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api")>()),
  getScopeAggregate: vi.fn(),
}));

const api = await import("@/lib/api");
const { AuthProvider } = await import("@/lib/auth/provider");
const { localReps } = await import("@/lib/votes/hooks");
const { UserAggregates, CellView } = await import("../bill/user-aggregates");

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function cell(scopeKey: string, overrides: Partial<AggregateCell> = {}): AggregateCell {
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

const data: BillAggregatesResponse = {
  bill_id: "hr-119-1",
  as_of: "2026-10-01T12:00:00Z",
  national: cell(""),
  states: [cell("CA", { yea_pct: 55, nay_pct: 45 }), cell("TX")],
  districts: [cell("CA-12", { status: "held" })],
};

function scope(scopeKey: string, overrides: Partial<ScopeAggregateResponse> = {}): ScopeAggregateResponse {
  return {
    bill_id: "hr-119-1",
    scope: scopeKey.includes("-") ? "district" : "state",
    scope_key: scopeKey,
    as_of: null,
    cell: null,
    members: [],
    ...overrides,
  };
}

const houseVote = {
  member_id: "P000197",
  first_name: "Nancy",
  last_name: "Pelosi",
  party: "D",
  congress: 119,
  vote: "yea",
  vote_id: "house-119-1-vote00010",
  chamber: "House",
  vote_date: "2025-03-04T18:00:00Z",
  question: "On Passage",
};

beforeEach(() => {
  localStorage.clear();
  localReps().clearReps();
  vi.mocked(api.getScopeAggregate).mockReset().mockImplementation(async (_bill, key) => scope(key));
});

describe("CellView", () => {
  it("shows the rounded numbers and when they were published", () => {
    const html = renderToStaticMarkup(<CellView cell={cell("")} />);
    expect(html).toContain("62%</span> Yea");
    expect(html).toContain("38%</span> Nay");
    expect(html).toContain("340+ users");
    expect(html).toContain("Published Oct 1, 2026.");
    expect(html).not.toContain("Under review");
  });

  it("marks a held cell as under review, keeping its last numbers", () => {
    const html = renderToStaticMarkup(<CellView cell={cell("CA-12", { status: "held" })} />);
    expect(html).toContain("Under review");
    expect(html).toContain("62%");
    expect(html).toContain("last published numbers");
  });

  it("says there aren't enough votes for a cell that isn't served", () => {
    expect(renderToStaticMarkup(<CellView cell={null} />)).toContain("Not enough votes yet.");
  });
});

describe("UserAggregates", () => {
  it("renders the national cell and the not-a-poll label on the server, with no browser fetch", () => {
    const html = renderToStaticMarkup(<UserAggregates billId="hr-119-1" data={data} />);
    expect(html).toContain("How Just a Bill users voted");
    expect(html).toContain("Not a poll.");
    expect(html).toContain('href="/methodology#aggregates"');
    expect(html).toContain("Nationwide");
    expect(html).toContain("62%");
    expect(api.getScopeAggregate).not.toHaveBeenCalled();
  });

  it("says there aren't enough votes nationwide when the national cell isn't served", () => {
    render(<UserAggregates billId="hr-119-1" data={{ ...data, national: null }} />);
    expect(screen.getByText("Not enough votes yet.")).toBeTruthy();
  });

  it("shows the visitor's saved district and state beside their members' votes", async () => {
    localReps().saveReps({
      state: "CA",
      district: 12,
      looked_up_at: "2026-09-30T00:00:00Z",
      members: [{ id: "P000197", name: "Nancy Pelosi", party: "D", chamber: "House", state: "CA", district: 12 }],
    });
    vi.mocked(api.getScopeAggregate).mockImplementation(async (_bill, key) =>
      key === "CA-12"
        ? scope(key, { cell: cell("CA-12", { status: "held" }), members: [houseVote] })
        : scope(key, { cell: cell("CA", { yea_pct: 55, nay_pct: 45 }) }),
    );
    render(<UserAggregates billId="hr-119-1" data={data} />);

    const district = screen.getByRole("region", { name: "Your district: CA-12" });
    expect(await within(district).findByText("Rep. Nancy Pelosi")).toBeTruthy();
    expect(within(screen.getByText("Rep. Nancy Pelosi").closest("li")!).getByText("Yea")).toBeTruthy();
    expect(within(district).getByText("Under review")).toBeTruthy();
    const state = screen.getByRole("region", { name: "Your state: CA" });
    expect(await within(state).findByText("No recorded vote on this bill from this seat yet.")).toBeTruthy();
    expect(within(state).getByText("55%")).toBeTruthy();
    expect(api.getScopeAggregate).toHaveBeenCalledWith("hr-119-1", "CA-12");
    expect(api.getScopeAggregate).toHaveBeenCalledWith("hr-119-1", "CA");

    // The picker leaves out the visitor's own constituencies.
    const options = within(screen.getByLabelText("Another state or district")).getAllByRole("option");
    expect(options.map((o) => o.textContent)).toEqual(["Choose one", "TX"]);
  });

  it("uses the signed-in account's district, the one their votes count in", async () => {
    localReps().saveReps({ state: "TX", district: 10, looked_up_at: "2026-09-30T00:00:00Z", members: [] });
    const { store } = fakeAuthStore("signed-in");
    render(
      <AuthProvider store={store}>
        <UserAggregates billId="hr-119-1" data={data} />
      </AuthProvider>,
    );
    expect(screen.getByRole("region", { name: "Your district: CA-12" })).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Your district: TX-10" })).toBeNull();
    expect(await screen.findAllByText("No recorded vote on this bill from this seat yet.")).toHaveLength(2);
  });

  it("says there aren't enough votes in the visitor's district while it isn't served", async () => {
    localReps().saveReps({ state: "NY", district: 3, looked_up_at: "2026-09-30T00:00:00Z", members: [] });
    render(<UserAggregates billId="hr-119-1" data={data} />);
    const district = screen.getByRole("region", { name: "Your district: NY-3" });
    expect(within(district).getByText("Not enough votes yet.")).toBeTruthy();
    expect(await within(district).findByText("No recorded vote on this bill from this seat yet.")).toBeTruthy();
  });

  it("shows a constituency picked from the list", async () => {
    vi.mocked(api.getScopeAggregate).mockImplementation(async (_bill, key) =>
      scope(key, { cell: cell(key, { yea_pct: 70, nay_pct: 30 }), members: [] }),
    );
    render(<UserAggregates billId="hr-119-1" data={data} />);
    const picker = screen.getByLabelText("A state or district");
    expect(within(picker).getAllByRole("option").map((o) => o.textContent)).toEqual([
      "Choose one",
      "CA",
      "TX",
      "CA-12",
    ]);
    fireEvent.change(picker, { target: { value: "TX" } });
    const picked = screen.getByRole("region", { name: "TX" });
    expect(await within(picked).findByText("70%")).toBeTruthy();
    expect(api.getScopeAggregate).toHaveBeenCalledWith("hr-119-1", "TX");
  });

  it("keeps the listed cell when the members' votes can't be loaded", async () => {
    vi.mocked(api.getScopeAggregate).mockRejectedValue(new Error("down"));
    render(<UserAggregates billId="hr-119-1" data={data} />);
    fireEvent.change(screen.getByLabelText("A state or district"), { target: { value: "CA" } });
    const picked = screen.getByRole("region", { name: "CA" });
    expect(await within(picked).findByText("We couldn't load how this seat's members voted.")).toBeTruthy();
    expect(within(picked).getByText("55%")).toBeTruthy();
  });

  it("has no axe violations", async () => {
    localReps().saveReps({ state: "CA", district: 12, looked_up_at: "2026-09-30T00:00:00Z", members: [] });
    vi.mocked(api.getScopeAggregate).mockImplementation(async (_bill, key) =>
      scope(key, { cell: cell(key), members: [houseVote] }),
    );
    const { container } = render(<UserAggregates billId="hr-119-1" data={data} />);
    await screen.findAllByText("Rep. Nancy Pelosi");
    expect(await axeViolations(container)).toEqual([]);
  });
});

describe("sharing a cell (#166)", () => {
  it("offers a share button on published cells only, not held ones", async () => {
    localReps().saveReps({ state: "CA", district: 12, looked_up_at: "2026-09-30T00:00:00Z", members: [] });
    vi.mocked(api.getScopeAggregate).mockImplementation(async (_bill, key) =>
      scope(key, { cell: key === "CA-12" ? cell(key, { status: "held" }) : cell(key) }),
    );
    render(<UserAggregates billId="hr-119-1" data={data} />);
    expect(screen.getByRole("button", { name: "Share how users nationwide voted" })).toBeTruthy();
    const state = screen.getByRole("region", { name: "Your state: CA" });
    expect(await within(state).findByRole("button", { name: "Share how users in CA voted" })).toBeTruthy();
    const district = screen.getByRole("region", { name: "Your district: CA-12" });
    expect(within(district).queryByRole("button", { name: /Share/ })).toBeNull();
  });

  it("shows no share button without a bill, or for a cell without numbers", () => {
    expect(renderToStaticMarkup(<CellView cell={cell("")} />)).not.toContain("Share");
    expect(renderToStaticMarkup(<CellView cell={null} billId="hr-119-1" />)).not.toContain("Share");
  });

  it("shares a link holding only the bill and the scope, and says the place may be the visitor's", async () => {
    const requested: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string | URL) => {
        requested.push(String(input));
        return new Response(new Uint8Array([0x89, 0x50, 0x4e, 0x47]), { headers: { "content-type": "image/png" } });
      }),
    );
    URL.createObjectURL = vi.fn(() => "blob:card");
    URL.revokeObjectURL = vi.fn();
    render(<UserAggregates billId="hr-119-1" data={data} />);
    fireEvent.click(screen.getByRole("button", { name: "Share how users nationwide voted" }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/if that's your own state or district, the link shows it/)).toBeTruthy();
    const link = within(dialog).getByLabelText("Link") as HTMLInputElement;
    expect(link.value).toBe(`${window.location.origin}/share/aggregate/hr-119-1/national`);
    const alt = "Just a Bill users nationwide: 62% Yea, 38% Nay on H.R. 1 (Just a Bill users, not a poll).";
    expect(await within(dialog).findByAltText(alt)).toBeTruthy();
    expect(requested).toEqual(["/share/aggregate/hr-119-1/national/image.png"]);
  });
});
