// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { BillSummary } from "../bill/bill-summary";
import { axeViolations } from "@/test/axe";

afterEach(cleanup);

const summary = {
  bill_id: "hr-119-1",
  short_summary: "Short.",
  long_summary: "Long.",
  who_it_affects: "Because.",
  model_used: "gemini-3.5-flash",
  source_version_code: "rh",
  source_version_name: "Reported in House",
  generated_at: "2026-09-01T00:00:00Z",
};

describe("BillSummary accessibility", () => {
  it("says whether the long summary is expanded", () => {
    render(<BillSummary summary={summary} />);
    const toggle = screen.getByRole("button", { name: "Read more" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(toggle);
    expect(screen.getByRole("button", { name: "Show less" }).getAttribute("aria-expanded")).toBe("true");
  });

  it("has no axe violations", async () => {
    const { container } = render(<BillSummary summary={summary} />);
    expect(await axeViolations(container)).toEqual([]);
  });
});
