// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { ActionTimeline } from "../bill/action-timeline";
import { billDetail } from "@/lib/examples";
import type { BillAction } from "@/lib/types";
import { axeViolations } from "@/test/axe";

// #664: the action timeline merges sources, shows the key actions first and the rest on Show all.

afterEach(cleanup);

const actions = billDetail.actions ?? [];

function entries(): HTMLElement[] {
  return within(screen.getByRole("list")).getAllByRole("listitem");
}

describe("ActionTimeline", () => {
  it("shows the key actions first, and says which they are", () => {
    render(<ActionTimeline actions={actions} />);
    expect(entries()).toHaveLength(6);
    expect(screen.getByText("6 key actions of 16: Congress.gov's major actions and roll-call votes.")).toBeTruthy();
    expect(screen.queryByText("Motion to reconsider laid on the table Agreed to without objection.")).toBeNull();
  });

  it("lists every action on Show all, and goes back", () => {
    render(<ActionTimeline actions={actions} />);
    const button = screen.getByRole("button", { name: "Show all 16 actions" });
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(button.getAttribute("aria-controls")).toBe(screen.getByRole("list").id);

    fireEvent.click(button);
    expect(entries()).toHaveLength(16);
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(button.textContent).toBe("Show key actions only");
    expect(screen.getByText("Motion to reconsider laid on the table Agreed to without objection.")).toBeTruthy();

    fireEvent.click(button);
    expect(entries()).toHaveLength(6);
  });

  it("shows an action recorded by two sources once, with both sources", () => {
    render(<ActionTimeline actions={actions} />);
    const presented = screen.getAllByText("Presented to President.");
    expect(presented).toHaveLength(1);
    const item = presented[0].closest("li") as HTMLElement;
    expect(item.textContent).toContain("Recorded by: House floor actions · Library of Congress");
    expect(within(item).getByText("Dec 18, 2025").tagName).toBe("TIME");
  });

  it("drops the raw action type badges", () => {
    render(<ActionTimeline actions={actions} />);
    expect(screen.queryByText("BecameLaw")).toBeNull();
    expect(screen.queryByText("IntroReferral")).toBeNull();
  });

  it("links an action's roll-call vote to the clerk's record", () => {
    const vote: BillAction = {
      id: "v",
      bill_id: "hr-119-1",
      action_date: "2026-03-03T00:00:00Z",
      action_text: "On passage Passed by the Yeas and Nays: 220 - 210 (Roll no. 99).",
      source_system: "House floor actions",
      recorded_vote: { roll_number: 99, url: "https://clerk.house.gov/Votes/202699", chamber: "House" },
      sort_order: 1,
    };
    render(<ActionTimeline actions={[vote]} />);
    const link = screen.getByRole("link", { name: "Roll call 99 (House)" });
    expect(link.getAttribute("href")).toBe("https://clerk.house.gov/Votes/202699");
  });

  it("shows everything, with no Show all, when Congress.gov has no overview to pick from", () => {
    const floorOnly = actions.filter((a) => a.source_system === "House floor actions");
    render(<ActionTimeline actions={floorOnly} />);
    expect(entries().length).toBeGreaterThan(0);
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.queryByText(/key actions of/)).toBeNull();
  });

  it("says so when there are no actions", () => {
    render(<ActionTimeline actions={[]} />);
    expect(screen.getByText("No actions recorded yet.")).toBeTruthy();
  });

  it("has no axe violations with the key actions or all of them", async () => {
    const { container } = render(<ActionTimeline actions={actions} />);
    expect(await axeViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Show all 16 actions" }));
    expect(await axeViolations(container)).toEqual([]);
  });
});
