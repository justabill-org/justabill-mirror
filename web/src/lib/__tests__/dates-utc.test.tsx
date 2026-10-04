// @vitest-environment jsdom
import { readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { formatDate, getRelativeTime } from "../utils";
import type { Bill, BillTextVersion, CompanionRollCall } from "../types";
import { SwipeCard } from "@/components/vote/swipe-card";
import { BillTextPanel } from "@/components/bill/bill-text-panel";
import { CompanionVotes } from "@/components/bill/companion-votes";

// The swipe card's Details reads the bill from the browser when it opens (#662); never answer.
vi.mock("@/lib/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api")>()),
  getBill: vi.fn(() => new Promise(() => {})),
}));

// #454: the API's dates are UTC midnight, so formatting them in a US zone showed the day before.
// Every test here runs in Los Angeles time, where that bug shows.
beforeAll(() => {
  vi.stubEnv("TZ", "America/Los_Angeles");
});
afterAll(() => {
  vi.unstubAllEnvs();
});
afterEach(cleanup);

const JAN_3 = "2025-01-03T00:00:00Z";

describe("formatDate in America/Los_Angeles", () => {
  it("runs in a zone where the local date is the day before (the bug's precondition)", () => {
    expect(new Date(JAN_3).toLocaleDateString("en-US", { month: "short", day: "numeric" })).toBe("Jan 2");
  });

  it("reads a UTC-midnight date as that day in every style", () => {
    expect(formatDate(JAN_3)).toBe("Jan 3, 2025");
    expect(formatDate(JAN_3, "long")).toBe("January 3, 2025");
    expect(formatDate(JAN_3, "short")).toBe("Jan 3");
    expect(formatDate(JAN_3, "weekday")).toBe("Friday, January 3, 2025");
    expect(formatDate(new Date(JAN_3))).toBe("Jan 3, 2025");
  });

  it("reads a Senate vote time (Eastern clock time marked UTC) as its day", () => {
    expect(formatDate("2025-01-03T22:15:00Z", "long")).toBe("January 3, 2025");
  });

  it("falls back to the UTC date for older times in getRelativeTime", () => {
    expect(getRelativeTime(JAN_3)).toBe("Jan 3");
  });
});

describe("components in America/Los_Angeles", () => {
  it("swipe card details show the real status and introduced dates", async () => {
    const bill = {
      id: "hr-119-1",
      congress: 119,
      bill_type: "hr",
      number: 1,
      title: "Companion Act",
      current_status: "introduced",
      status_date: JAN_3,
      introduced_date: "2025-01-06T00:00:00Z",
      updated_at: "2025-01-07T00:00:00Z",
    } as Bill;
    // The sheet reads the bill's record when it opens (#717).
    vi.stubGlobal("fetch", (url: string) =>
      Promise.resolve(
        String(url).endsWith("/law-changes")
          ? { ok: false, status: 404, statusText: "Not Found", text: () => Promise.resolve("") }
          : { ok: true, status: 200, json: () => Promise.resolve({ bill, votes: [], actions: [] }) },
      ),
    );
    render(<SwipeCard bill={bill} summary={null} onVote={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: "Details" }));
    await screen.findByText("Where it stands");
    const text = screen.getByRole("dialog").textContent;
    expect(text).toContain("since Jan 3, 2025");
    expect(text).toContain("Introduced Jan 6, 2025");
    vi.unstubAllGlobals();
  });

  it("text versions show the version's date", () => {
    const version: BillTextVersion = {
      id: "v1",
      bill_id: "hr-119-1",
      version_type: "Introduced in House",
      version_code: "ih",
      date: JAN_3,
      formats: [],
      sort_order: 1,
    };
    const html = renderToStaticMarkup(
      <BillTextPanel billId="hr-119-1" billType="hr" versions={[version]} diffs={[]} actions={[]} related={[]} />,
    );
    expect(html).toContain("January 3, 2025");
    expect(html).not.toContain("January 2, 2025");
  });

  it("companion votes show a House roll call's date (stored as UTC midnight)", () => {
    const rollCall: CompanionRollCall = {
      companion_bill_id: "s-119-5",
      vote_id: "house-119-1-12",
      chamber: "House",
      vote_date: JAN_3,
      votes: [],
    };
    const html = renderToStaticMarkup(<CompanionVotes rollCalls={[rollCall]} />);
    expect(html).toContain("January 3, 2025");
    expect(html).not.toContain("January 2, 2025");
  });
});

/** Every .ts and .tsx file under dir, tests left out. */
function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const full = path.join(dir, name);
    if (statSync(full).isDirectory()) return name === "__tests__" ? [] : sources(full);
    return /\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) ? [full] : [];
  });
}

describe("one date helper", () => {
  // Places that format a date themselves, and why that's right there.
  const allowed = new Set([
    "lib/utils.ts", // formatDate itself
    "lib/election-district.ts", // builds the date with Date.UTC and formats it in UTC
    "app/(app)/settings/settings-form.tsx", // a real instant (next district change), in the reader's zone
    "components/votes/compact-vote-row.tsx", // a real instant (when you voted), in the reader's zone
  ]);

  it("formats dates only through formatDate", () => {
    const src = path.resolve(__dirname, "../..");
    const offenders = sources(src)
      .filter((file) => readFileSync(file, "utf8").includes("toLocaleDateString("))
      .map((file) => path.relative(src, file))
      .filter((file) => !allowed.has(file));
    expect(offenders).toEqual([]);
  });
});
