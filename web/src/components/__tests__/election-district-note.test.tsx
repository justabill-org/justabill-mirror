// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { ElectionDistrictNote } from "../member/election-district-note";
import type { ElectionLookup } from "@/lib/election-district";
import { axeViolations } from "@/test/axe";

afterEach(cleanup);

const changed: ElectionLookup = {
  districts: [{ state: "TX", district: 37 }],
  election: { congress: 120, election_date: "2026-11-03", districts: [{ state: "TX", district: 10 }], changed: true },
};
const today = new Date(2026, 9, 4, 12);

describe("ElectionDistrictNote", () => {
  it("shows the line, the source and a vote.gov link that opens in a new tab", async () => {
    const { container } = render(<ElectionDistrictNote lookup={changed} today={today} />);

    expect(screen.getByText("TX-10").tagName).toBe("STRONG");
    expect(container.textContent).toContain(
      "On November 3, 2026, this address votes in the election for TX-10. District lines changed for 2026, " +
        "so this isn't the district your current representative holds (TX-37)."
    );
    expect(container.textContent).toContain(
      "Map: U.S. Census Bureau, 120th Congress districts as submitted by the state. " +
        "Confirm with your state election office at vote.gov."
    );
    const link = screen.getByRole("link", { name: "vote.gov" });
    expect(link.getAttribute("href")).toBe("https://vote.gov");
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener noreferrer");
    expect(await axeViolations(container)).toEqual([]);
  });

  it.each([
    ["the districts didn't change", { ...changed, election: { ...changed.election!, changed: false } }],
    ["there's no election block", { districts: changed.districts }],
  ])("renders nothing when %s", (_, lookup) => {
    const { container } = render(<ElectionDistrictNote lookup={lookup} today={today} />);
    expect(container.innerHTML).toBe("");
    expect(screen.queryByRole("link")).toBeNull();
  });
});
