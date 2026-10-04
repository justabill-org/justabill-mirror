// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { axeViolations } from "@/test/axe";
import { billsBecameLaw, congresses } from "@/lib/examples";
import type { BillListItem } from "@/lib/types";
import { BillListCard } from "../bill/bill-list-card";
import { BillListControls } from "../bill/bill-list-controls";

// The /bills cards and controls (#666): quick views with counts, the sort, a card's one-line
// description, progress and sponsor.

const nav = vi.hoisted(() => ({ query: "", push: vi.fn() }));
const auth = vi.hoisted(() => ({ status: "signed-out" }));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: nav.push }),
  useSearchParams: () => new URLSearchParams(nav.query),
}));
vi.mock("@/lib/auth/provider", () => ({ useUser: () => ({ status: auth.status }) }));

afterEach(() => {
  cleanup();
  nav.query = "";
  nav.push.mockReset();
  auth.status = "signed-out";
  vi.unstubAllEnvs();
});

const law = billsBecameLaw.items[0];

function item(fields: Partial<BillListItem> = {}): BillListItem {
  return {
    ...law,
    summary: { short_summary: "Designates a memorial site. It also transfers land." } as BillListItem["summary"],
    ...fields,
  };
}

describe("BillListCard", () => {
  it("shows the AI summary's first sentence, labeled as AI, instead of the latest action", () => {
    const { container } = render(<BillListCard bill={item()} sort="latest_action" />);
    expect(container.textContent).toContain("AI summary: Designates a memorial site.");
    expect(container.textContent).not.toContain("It also transfers land.");
    expect(container.textContent).not.toContain(law.latest_action?.text ?? "never");
  });

  it("links the bill by its title, with a Share button for it outside the link (#811)", () => {
    render(<BillListCard bill={item()} sort="latest_action" />);
    const link = screen.getByRole("link");
    expect(link.getAttribute("href")).toBe("/bills/hr-119-165");
    expect(link.textContent).toBe("Wounded Knee Massacre Memorial and Sacred Site Act");
    // A button inside a link is invalid HTML, and pressing it would also follow the link.
    const share = screen.getByRole("button", { name: "Share H.R. 165" });
    expect(link.contains(share)).toBe(false);
    expect(share.closest("a")).toBeNull();
  });

  it("shares the bill's own page from the list, not the list's URL with its filters (#811)", () => {
    window.history.replaceState(null, "", "/bills?show=all&policy_area=Health&page=3#results");
    render(<BillListCard bill={item()} sort="latest_action" />);
    fireEvent.click(screen.getByRole("button", { name: "Share H.R. 165" }));
    const sheet = screen.getByRole("dialog", { name: "Share this bill" });
    expect(within(sheet).getByLabelText("Link")).toHaveProperty("value", `${window.location.origin}/bills/hr-119-165`);
    expect(sheet.textContent).toContain("H.R. 165: Wounded Knee Massacre Memorial and Sacred Site Act");
    // Pressing Share leaves the reader on the list.
    expect(window.location.pathname + window.location.search).toBe("/bills?show=all&policy_area=Health&page=3");
  });

  it("falls back to the CRS summary's first sentence, labeled as CRS, never as AI (#714)", () => {
    const crs_summary = {
      version_code: "00",
      action_date: "2025-01-03",
      action_desc: "Introduced in House",
      lead: "This bill designates a memorial site. It also transfers land.",
    };
    const { container } = render(<BillListCard bill={item({ summary: null, crs_summary })} sort="latest_action" />);
    expect(container.textContent).toContain("CRS summary: This bill designates a memorial site.");
    expect(container.textContent).not.toContain("It also transfers land.");
    expect(screen.queryByText(/AI summary/)).toBeNull();
  });

  it("says when a bill has no summary yet", () => {
    render(<BillListCard bill={item({ summary: null })} sort="latest_action" />);
    expect(screen.getByText("No plain-language summary yet.")).toBeTruthy();
    expect(screen.queryByText(/AI summary/)).toBeNull();
  });

  it("shows how far the bill got, in words for screen readers", () => {
    render(<BillListCard bill={item({ current_status: "passed_senate" })} sort="latest_action" />);
    expect(screen.getByText(/Progress: step 2 of 4,/).parentElement?.textContent).toBe(
      "Progress: step 2 of 4, Passed one chamber",
    );
  });

  it("shows a law's date instead of a full progress bar", () => {
    const { container } = render(<BillListCard bill={item()} sort="latest_action" />);
    expect(screen.queryByText(/Progress: step/)).toBeNull();
    expect(container.textContent).toContain("Became law Dec 19, 2025");
  });

  it("names the sponsor with party and state", () => {
    const { container } = render(<BillListCard bill={item()} sort="latest_action" />);
    expect(container.textContent).toContain("Rep. Johnson, Dusty (R-SD)");
  });

  it("shows the date the list is sorted by", () => {
    const { container, rerender } = render(
      <BillListCard bill={item({ current_status: "passed_house" })} sort="latest_action" />,
    );
    expect(container.textContent).toContain("Latest action Dec 19, 2025");
    rerender(<BillListCard bill={item()} sort="introduced_date" />);
    expect(container.textContent).toContain("Introduced Jan 3, 2025");
  });

  it("has no axe violations", async () => {
    const { container } = render(
      <ul>
        <li>
          <BillListCard bill={item()} sort="latest_action" />
        </li>
        <li>
          <BillListCard bill={item({ id: "hr-119-2", summary: null })} sort="introduced_date" />
        </li>
      </ul>,
    );
    expect(await axeViolations(container)).toEqual([]);
  });
});

const counts = { laws: 2737, passed: 512, committee: 16796, all: 19441 };

describe("BillListControls", () => {
  it("shows the four views with their counts, Laws first and chosen by default", () => {
    render(<BillListControls congresses={congresses} counts={counts} />);
    const views = within(screen.getByRole("navigation", { name: "Bill views" })).getAllByRole("link");
    expect(views.map((v) => v.textContent)).toEqual([
      "Laws, 2,737 bills",
      "Passed a chamber, 512 bills",
      "In committee, 16,796 bills",
      "All, 19,441 bills",
    ]);
    expect(views[0].getAttribute("aria-current")).toBe("true");
    expect(views.slice(1).every((v) => !v.hasAttribute("aria-current"))).toBe(true);
  });

  it("links each view, keeping the other filters and going back to the first page", () => {
    nav.query = "show=committee&q=tax&congress=119&offset=24";
    render(<BillListControls congresses={congresses} counts={counts} />);
    const views = within(screen.getByRole("navigation", { name: "Bill views" })).getAllByRole("link");
    expect(views.map((v) => v.getAttribute("href"))).toEqual([
      "/bills?congress=119&q=tax",
      "/bills?show=passed&congress=119&q=tax",
      "/bills?show=committee&congress=119&q=tax",
      "/bills?show=all&congress=119&q=tax",
    ]);
    expect(views[2].getAttribute("aria-current")).toBe("true");
  });

  it("leaves out a count it doesn't have", () => {
    render(<BillListControls congresses={congresses} counts={{}} />);
    const views = within(screen.getByRole("navigation", { name: "Bill views" })).getAllByRole("link");
    expect(views.map((v) => v.textContent)).toEqual(["Laws", "Passed a chamber", "In committee", "All"]);
  });

  it("sorts by latest action or by date introduced, under Filters", () => {
    nav.query = "show=passed";
    render(<BillListControls congresses={congresses} counts={counts} />);
    expect(screen.queryByLabelText("Sort by")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
    const sort = screen.getByLabelText("Sort by") as HTMLSelectElement;
    expect([...sort.options].map((o) => o.text)).toEqual(["Latest action", "Date introduced"]);
    expect(sort.value).toBe("latest_action");
    fireEvent.change(sort, { target: { value: "introduced_date" } });
    expect(nav.push).toHaveBeenCalledWith("/bills?show=passed&sort=introduced_date");
  });

  it("puts the title, search, filters and views in one bar, search first", () => {
    vi.useFakeTimers();
    nav.query = "show=passed";
    render(<BillListControls congresses={congresses} counts={counts} />);
    const search = screen.getByRole("searchbox", { name: "Search bills" });
    expect(document.querySelector("input, select, a")).toBe(search);
    const bar = search.closest(".sticky") as HTMLElement;
    expect(within(bar).getByRole("heading", { level: 1, name: "Bills" })).toBeTruthy();
    expect(within(bar).getByRole("button", { name: /Filters/ })).toBeTruthy();
    expect(within(bar).getByRole("navigation", { name: "Bill views" })).toBeTruthy();
    fireEvent.change(search, { target: { value: "veterans" } });
    vi.advanceTimersByTime(300);
    expect(nav.push).toHaveBeenCalledWith("/bills?show=passed&q=veterans");
    vi.useRealTimers();
  });

  it("keeps congress, bill type and chamber behind Filters", () => {
    render(<BillListControls congresses={congresses} counts={counts} />);
    expect(screen.queryByLabelText("Congress")).toBeNull();
    const more = screen.getByRole("button", { name: /Filters/ });
    fireEvent.click(more);
    expect(more.getAttribute("aria-expanded")).toBe("true");
    fireEvent.change(screen.getByLabelText("Chamber of origin"), { target: { value: "senate" } });
    expect(nav.push).toHaveBeenCalledWith("/bills?chamber=senate");
    expect(screen.queryByLabelText(/haven't voted on/)).toBeNull();
  });

  it("shows the current congress by default, and another or all of them from Filters (#717)", () => {
    const two = [{ ...congresses[0], number: 118, is_current: false }, ...congresses];
    const { unmount } = render(<BillListControls congresses={two} counts={counts} />);
    const more = screen.getByRole("button", { name: /Filters/ });
    expect(more.textContent).toBe("Filters");
    fireEvent.click(more);
    const select = screen.getByLabelText("Congress") as HTMLSelectElement;
    expect([...select.options].map((o) => o.text)).toEqual(["118th", "119th (current)", "All congresses"]);
    expect(select.value).toBe("119");
    fireEvent.change(select, { target: { value: "all" } });
    expect(nav.push).toHaveBeenLastCalledWith("/bills?congress=all");
    fireEvent.change(select, { target: { value: "118" } });
    expect(nav.push).toHaveBeenLastCalledWith("/bills?congress=118");
    unmount();

    nav.query = "congress=all";
    render(<BillListControls congresses={two} counts={counts} />);
    expect(screen.getByRole("button", { name: /Filters/ }).textContent).toBe("Filters1 active");
    fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
    fireEvent.change(screen.getByLabelText("Congress"), { target: { value: "119" } });
    expect(nav.push).toHaveBeenLastCalledWith("/bills");
  });

  it("offers bills I haven't voted on only to a signed-in user with accounts on", () => {
    vi.stubEnv("NEXT_PUBLIC_ACCOUNTS_ENABLED", "true");
    auth.status = "signed-in";
    render(<BillListControls congresses={congresses} counts={counts} />);
    fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
    fireEvent.click(screen.getByLabelText("Show only bills I haven't voted on"));
    expect(nav.push).toHaveBeenCalledWith("/bills?unvoted=true");
  });

  it("filters by policy area under Filters, and counts it on the button (#796)", () => {
    nav.query = "show=passed";
    const { unmount } = render(
      <BillListControls congresses={congresses} counts={counts} policyAreas={["Health", "Taxation"]} />,
    );
    fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
    const area = screen.getByLabelText("Policy area") as HTMLSelectElement;
    expect([...area.options].map((o) => o.text)).toEqual(["All", "Health", "Taxation"]);
    expect(area.value).toBe("");
    fireEvent.change(area, { target: { value: "Taxation" } });
    expect(nav.push).toHaveBeenCalledWith("/bills?show=passed&area=Taxation");
    unmount();

    nav.query = "show=passed&area=Taxation&offset=12";
    render(<BillListControls congresses={congresses} counts={counts} policyAreas={["Health", "Taxation"]} />);
    const button = screen.getByRole("button", { name: /Filters/ });
    expect(button.textContent).toBe("Filters1 active");
    fireEvent.click(button);
    expect((screen.getByLabelText("Policy area") as HTMLSelectElement).value).toBe("Taxation");
    fireEvent.change(screen.getByLabelText("Policy area"), { target: { value: "" } });
    expect(nav.push).toHaveBeenLastCalledWith("/bills?show=passed");
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(nav.push).toHaveBeenLastCalledWith("/bills?show=passed");
  });

  it("keeps an area the URL names but the list doesn't have selected", () => {
    nav.query = "area=Nope";
    render(<BillListControls congresses={congresses} counts={counts} policyAreas={["Health"]} />);
    fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
    const area = screen.getByLabelText("Policy area") as HTMLSelectElement;
    expect(area.value).toBe("Nope");
    expect([...area.options].map((o) => o.text)).toEqual(["All", "Nope", "Health"]);
  });

  it.each([
    ["couldn't be read", null],
    ["are none (an empty database)", []],
  ])("leaves the policy area out when the areas %s", (_, policyAreas) => {
    render(<BillListControls congresses={congresses} counts={counts} policyAreas={policyAreas} />);
    fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
    expect(screen.queryByLabelText("Policy area")).toBeNull();
    expect(screen.getByLabelText("Bill type")).toBeTruthy();
  });

  it("keeps the page size and Unvoted only when a filter changes", () => {
    vi.stubEnv("NEXT_PUBLIC_ACCOUNTS_ENABLED", "true");
    auth.status = "signed-in";
    nav.query = "unvoted=true&limit=24&offset=48&type=zz";
    render(<BillListControls congresses={congresses} counts={counts} />);
    fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
    fireEvent.change(screen.getByLabelText("Chamber of origin"), { target: { value: "house" } });
    expect(nav.push).toHaveBeenLastCalledWith("/bills?chamber=house&limit=24&unvoted=true");
    fireEvent.click(screen.getByLabelText("Show only bills I haven't voted on"));
    expect(nav.push).toHaveBeenLastCalledWith("/bills?limit=24");
  });

  it("has no axe violations, with Filters open", async () => {
    const { container } = render(
      <BillListControls congresses={congresses} counts={counts} policyAreas={["Health", "Taxation"]} />,
    );
    expect(await axeViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
    expect(await axeViolations(container)).toEqual([]);
  });
});
