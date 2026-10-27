// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { axeViolations } from "@/test/axe";
import { AmendmentsList } from "../bill/amendments-list";
import type { Amendment } from "@/lib/types";

// The bill page's Amendments tab (#872): an honest empty state, and each amendment's facts with
// its links.

afterEach(cleanup);

const samdt: Amendment = {
  id: "samdt-119-12",
  bill_id: "hr-119-1",
  congress: 119,
  amendment_type: "SAMDT",
  amendment_number: 12,
  purpose: "To strike section 2.",
  sponsor_id: "S000001",
  latest_action: { actionDate: "2025-03-04T00:00:00Z", text: "Amendment SA 12 agreed to in Senate by Voice Vote." },
  submitted_date: "2025-03-01T00:00:00Z",
  chamber: "Senate",
  synced_at: "2025-03-05T00:00:00Z",
};

const hamdt: Amendment = {
  id: "hamdt-119-40",
  bill_id: "hr-119-1",
  congress: 119,
  amendment_type: "hamdt",
  amendment_number: 40,
  description: "An amendment numbered 3 printed in House Report 119-5.",
  purpose: "Not shown when there's a description.",
  chamber: "House",
};

/** The amendment entries, in the order they're shown, by their designation. */
function amendmentIds(): string[] {
  return screen.getAllByText(/^(S|H)\.Amdt\. \d+$/).map((el) => el.textContent ?? "");
}

describe("AmendmentsList", () => {
  it("says no amendments have been proposed when there are none", () => {
    render(<AmendmentsList amendments={[]} />);
    expect(screen.getByText("No amendments have been proposed for this bill.")).toBeTruthy();
  });

  it("shows one amendment's designation, chamber, dates, purpose, sponsor and latest action", () => {
    render(<AmendmentsList amendments={[samdt]} />);
    expect(amendmentIds()).toEqual(["S.Amdt. 12"]);
    expect(screen.getByText("Senate")).toBeTruthy();
    expect(screen.getByText("Submitted Mar 1, 2025")).toBeTruthy();
    expect(screen.getByText("To strike section 2.")).toBeTruthy();
    expect(screen.getByRole("link", { name: "View Sponsor" }).getAttribute("href")).toBe("/members/S000001");
    expect(screen.getByText("Latest Action - Mar 4, 2025")).toBeTruthy();
    expect(screen.getByText("Amendment SA 12 agreed to in Senate by Voice Vote.")).toBeTruthy();
    expect(screen.queryByText(/No amendments/)).toBeNull();
  });

  it("lists many newest first, preferring the description to the purpose, without a sponsor link it can't make", () => {
    const old = { ...samdt, id: "samdt-119-3", amendment_number: 3 };
    render(<AmendmentsList amendments={[old, hamdt, samdt]} />);
    expect(amendmentIds()).toEqual(["H.Amdt. 40", "S.Amdt. 12", "S.Amdt. 3"]);
    expect(screen.getByText("An amendment numbered 3 printed in House Report 119-5.")).toBeTruthy();
    expect(screen.queryByText("Not shown when there's a description.")).toBeNull();
    expect(screen.getAllByRole("link", { name: "View Sponsor" })).toHaveLength(2);
  });

  it("links each designation to the amendment's Congress.gov page in a new tab", () => {
    render(<AmendmentsList amendments={[samdt, hamdt]} />);
    for (const [name, href] of [
      ["S.Amdt. 12 on Congress.gov (opens in a new tab)", "https://www.congress.gov/amendment/119th-congress/senate-amendment/12"],
      ["H.Amdt. 40 on Congress.gov (opens in a new tab)", "https://www.congress.gov/amendment/119th-congress/house-amendment/40"],
    ]) {
      const link = screen.getByRole("link", { name });
      expect(link.getAttribute("href")).toBe(href);
      expect(link.getAttribute("target")).toBe("_blank");
      expect(link.getAttribute("rel")).toBe("noopener noreferrer");
      expect(link.textContent).toBe(name.split(" on ")[0]);
    }
  });

  it("prints an unknown amendment type as it came, upper-cased, with no link", () => {
    render(<AmendmentsList amendments={[{ ...hamdt, amendment_type: "suamdt", sponsor_id: undefined }]} />);
    expect(screen.getByText("SUAMDT 40")).toBeTruthy();
    expect(screen.queryAllByRole("link")).toEqual([]);
  });

  it("has no axe violations", async () => {
    const { container } = render(<AmendmentsList amendments={[samdt, hamdt]} />);
    expect(await axeViolations(container)).toEqual([]);
  });
});
