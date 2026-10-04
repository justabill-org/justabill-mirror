// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { axeViolations } from "@/test/axe";
import type { Bill, BillListItem, Congress, PaginatedResult } from "../types";

// #717: the home page says what the site is and shows the record: the newest law in full, with its
// AI summary labeled as AI, then the congress's counts linking to the /bills views that list them.

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  listBills: vi.fn(),
  listBillsWithSummaries: vi.fn(),
  listCongresses: vi.fn(),
}));

const api = await import("../api");
const examples = await import("../examples");
const { default: Home } = await import("@/app/page");

const congresses: Congress[] = [{ number: 120, start_date: "2027-01-03", is_current: true }];

function law(number: number, title: string, summary: string | null): BillListItem {
  const bill: Bill = { ...examples.billsBecameLaw.items[0], id: `hr-120-${number}`, congress: 120, number, title };
  return {
    ...bill,
    latest_action: { actionDate: "2027-03-02T00:00:00Z", text: "Became Public Law No: 120-1." },
    summary: summary === null ? null : { ...examples.billDetail.summary!, short_summary: summary },
  };
}

function page<T>(items: T[], total = items.length): PaginatedResult<T> {
  return { items, total, offset: 0, limit: items.length };
}

/** The page as a browser has it, for queries and axe. */
async function renderHome(): Promise<HTMLElement> {
  document.body.innerHTML = renderToStaticMarkup(await Home());
  return document.body;
}

beforeEach(() => {
  vi.mocked(api.listCongresses).mockReset().mockResolvedValue(congresses);
  vi.mocked(api.listBills).mockReset().mockResolvedValue(page([], 7));
  vi.mocked(api.listBillsWithSummaries)
    .mockReset()
    .mockResolvedValue(page([law(1, "Newest Law Act", "Would rename a post office."), law(2, "Older Law Act", null)]));
  vi.spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("home page (#717)", () => {
  it("opens with what the site is, in one h1, not a slogan", async () => {
    const body = await renderHome();
    const h1s = body.querySelectorAll("h1");
    expect(h1s).toHaveLength(1);
    expect(h1s[0].textContent).toBe("What Congress is doing, in plain language.");
    expect(body.textContent).not.toMatch(/Hold Congress accountable|Democracy, simplified|make your voice heard/i);
  });

  it("shows the newest law in full, its AI summary labeled as AI next to it", async () => {
    const body = await renderHome();
    const featured = body.querySelector("article")!;
    expect(featured.querySelector("h3")?.textContent).toBe("Newest Law Act");
    expect(featured.textContent).toContain("Became Public Law No: 120-1.");
    expect(featured.textContent).toContain("AI summary: Would rename a post office.");
    expect(featured.querySelector('a[href="/bills/hr-120-1"]')?.textContent).toBe("Newest Law Act");
    // The rest are cards below it, their titles a level under the section's.
    expect(body.querySelector('li h3 a[href="/bills/hr-120-2"]')?.textContent).toBe("Older Law Act");
    // Each card can be shared without opening it (#811).
    expect(body.querySelector('li button[aria-label="Share H.R. 2"]')?.closest("a")).toBeNull();
  });

  it("says when the newest law has no summary yet, rather than showing another's", async () => {
    vi.mocked(api.listBillsWithSummaries).mockResolvedValue(page([law(3, "Unsummarized Act", null)]));
    const featured = (await renderHome()).querySelector("article")!;
    expect(featured.textContent).toContain("No plain-language summary yet.");
    expect(featured.textContent).not.toContain("AI summary:");
  });

  it("counts the congress the way /bills does, each count linking to its view", async () => {
    const body = await renderHome();
    const counts = body.querySelector('section[aria-labelledby="counts-title"]')!;
    expect(counts.querySelector("h2")?.textContent).toBe("The 120th Congress so far");
    const links = [...counts.querySelectorAll("dd a")].map((a) => [a.getAttribute("href"), a.textContent]);
    // Every bill is one call; "passed a chamber" and "became law" sum one call per status of their view.
    expect(links.map(([href]) => href)).toEqual(["/bills?show=all", "/bills?show=passed", "/bills"]);
    expect(links[0][1]).toBe("7");
    expect(api.listBills).toHaveBeenCalledWith({ congress: 120, limit: 1 });
  });

  it("uses sentence case and one name for each action", async () => {
    const body = await renderHome();
    const actions = [...body.querySelectorAll("main a")].map((a) => a.textContent);
    expect(actions).toContain("Start voting");
    expect(actions).toContain("Browse bills");
    expect(actions).not.toContain("Start Voting");
    const headings = [...body.querySelectorAll("main h2")].map((h) => h.textContent);
    expect(headings).toEqual([
      "Recently became law",
      "The 120th Congress so far",
      "How it works",
      "Where the data comes from",
    ]);
  });

  it("has no axe violations, with the laws and with them unavailable", async () => {
    expect(await axeViolations(await renderHome())).toEqual([]);
    vi.mocked(api.listBillsWithSummaries).mockRejectedValue(new TypeError("fetch failed"));
    vi.mocked(api.listBills).mockRejectedValue(new TypeError("fetch failed"));
    expect(await axeViolations(await renderHome())).toEqual([]);
  });
});
