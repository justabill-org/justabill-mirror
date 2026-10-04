// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { StatusProgress } from "../bill/status-progress";
import { billDetail } from "@/lib/examples";
import { axeViolations } from "@/test/axe";

// #664: the bill page's Legislative progress, with dates, skipped steps and the current one.

afterEach(cleanup);

function renderHr187() {
  const { bill, status_history } = billDetail;
  return render(
    <StatusProgress
      billType={bill.bill_type}
      currentStatus={bill.current_status}
      statusHistory={status_history ?? []}
      introducedDate={bill.introduced_date}
    />,
  );
}

function step(label: string): HTMLElement {
  const item = screen.getByText(label, { selector: "p" }).closest("li");
  if (!item) throw new Error(`no step ${label}`);
  return item;
}

describe("StatusProgress", () => {
  it("shows each reached step's date", () => {
    renderHr187();
    const introduced = within(step("Introduced")).getByText("Jan 3, 2025");
    expect(introduced.tagName).toBe("TIME");
    expect(introduced.getAttribute("dateTime")).toBe("2025-01-03");
    expect(within(step("Passed Senate")).getByText("Dec 16, 2025")).toBeTruthy();
    expect(within(step("Signed")).getByText("Dec 26, 2025")).toBeTruthy();
  });

  it("says a skipped step was skipped, and a step with no date that its date isn't recorded", () => {
    renderHr187();
    expect(within(step("Reported")).getByText("Skipped")).toBeTruthy();
    expect(within(step("Resolving differences")).getByText("Skipped")).toBeTruthy();
    expect(within(step("Passed House")).getByText("Date not recorded")).toBeTruthy();
  });

  it("says an original measure began in committee, and no step says a date is missing (#781)", async () => {
    // hres-119-1530: reported by the Rules Committee as an original measure, never referred.
    const { container } = render(
      <StatusProgress
        billType="hres"
        currentStatus="passed_house"
        statusHistory={[
          { status: "reported", status_date: "2026-09-14T00:00:00Z", status_rank: 3 },
          { status: "passed_house", status_date: "2026-09-15T00:00:00Z", status_rank: 4 },
        ]}
        introducedDate="2026-09-14T00:00:00Z"
      />,
    );
    const committee = step("In committee");
    // In text, not only the check mark: the dot is aria-hidden.
    expect(within(committee).getByText("Began in committee")).toBeTruthy();
    expect(committee.querySelector("svg")).not.toBeNull();
    expect(screen.queryByText("Date not recorded")).toBeNull();
    expect(await axeViolations(container)).toEqual([]);
  });

  it("marks the current step in text and with aria-current, not by color alone", () => {
    renderHr187();
    const current = screen.getAllByRole("listitem").filter((li) => li.getAttribute("aria-current") === "step");
    expect(current).toHaveLength(1);
    expect(current[0]).toBe(step("Became law"));
    expect(within(current[0]).getByText("Current step")).toBeTruthy();
    expect(screen.getAllByText("Current step")).toHaveLength(1);
  });

  it("tells screen readers an upcoming step hasn't happened yet", () => {
    render(
      <StatusProgress
        billType="s"
        currentStatus="introduced"
        statusHistory={[{ status: "introduced", status_date: "2026-03-02T00:00:00Z", status_rank: 1 }]}
      />,
    );
    const upcoming = within(step("Passed House")).getByText("Not yet");
    expect(upcoming.className).toContain("sr-only");
    // A Senate bill passes the Senate first.
    const labels = screen.getAllByRole("listitem").map((li) => li.querySelector("p")?.textContent);
    expect(labels.indexOf("Passed Senate")).toBeLessThan(labels.indexOf("Passed House"));
  });

  it("is a vertical list below the lg breakpoint and a row from lg, so phone labels never overlap", () => {
    renderHr187();
    const list = screen.getByRole("list");
    expect(list.tagName).toBe("OL");
    expect(list.className).toMatch(/(^|\s)flex-col(\s|$)/);
    expect(list.className).toContain("lg:flex-row");
    expect(within(list).getAllByRole("listitem")).toHaveLength(9);
  });

  it("draws the progress in ink, not green or the link color (#764)", () => {
    const { container } = renderHr187();
    // Introduced (reached), Passed House (reached, no date), Became law (current).
    for (const label of ["Introduced", "Passed House", "Became law"]) {
      const dot = step(label).querySelector('span[aria-hidden="true"].rounded-full.border-2');
      expect(dot?.className, label).toContain("border-foreground");
    }
    expect(within(step("Became law")).getByText("Current step").className).toContain("bg-primary");
    const colors = [...container.querySelectorAll("[class]")].map((el) => el.getAttribute("class")).join(" ");
    expect(colors).not.toMatch(/(?:bg|text|border|ring)-(?:success|link|accent)/);
  });

  it("has no axe violations", async () => {
    const { container } = renderHr187();
    expect(await axeViolations(container)).toEqual([]);
  });
});
