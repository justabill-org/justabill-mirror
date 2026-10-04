// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { axeViolations } from "@/test/axe";
import type { BillLawChangesResponse, LawSectionResponse } from "@/lib/types";

vi.mock("@/lib/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api")>()),
  getLawSection: vi.fn(),
}));
vi.mock("@/lib/obs/browser", () => ({ reportError: vi.fn() }));

const api = await import("@/lib/api");
const { LawChanges } = await import("../bill/law-changes");

afterEach(cleanup);

const data: BillLawChangesResponse = {
  bill_id: "hr-119-1",
  version_id: "v",
  version_code: "ih",
  explained: { model_used: "m", prompt_version: "p", generated_at: "2026-09-29T00:00:00Z", release_point: "119-111" },
  ai_generated: true,
  current_release_point: { release_point: "119-111", source_url: "" },
  changes: [
    {
      section_id: "/us/usc/t42/s1395w-4",
      in_us_code: true,
      loaded: true,
      title_number: 42,
      section_number: "1395w-4",
      heading: "Payment",
      change_kind: "amends",
      cite_text: null,
      subsection_path: "(t)",
      instruction: "Strike 2026, insert 2027.",
      explanation: "Moves a date.",
      also_changed_by: [
        { bill_id: "s-119-40", congress: 119, bill_type: "s", number: 40, title: "Other", ref_kinds: ["amends"] },
      ],
    },
  ],
};

const section: LawSectionResponse = {
  section_id: "/us/usc/t42/s1395w-4",
  title_number: 42,
  section_number: "1395w-4",
  heading: "Payment",
  text: "(a) The current text of the section.",
  status: "",
  positive_law: false,
  release_point: "119-111",
  updated_at: "2026-09-24T00:00:00Z",
  current_release_point: null,
};

beforeEach(() => {
  vi.mocked(api.getLawSection).mockReset();
});

describe("LawChanges accessibility and current text", () => {
  it("has no axe violations, collapsed or expanded", async () => {
    vi.mocked(api.getLawSection).mockResolvedValue(section);
    const { container } = render(<LawChanges data={data} />);
    expect(await axeViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Show current text of 42 U.S.C. 1395w-4" }));
    await screen.findByText("(a) The current text of the section.");
    expect(await axeViolations(container)).toEqual([]);
  });

  it("fetches the current text once, on first expand", async () => {
    vi.mocked(api.getLawSection).mockResolvedValue(section);
    render(<LawChanges data={data} />);
    const toggle = screen.getByRole("button", { name: /^Show current text/ });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(api.getLawSection).not.toHaveBeenCalled();

    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("status").textContent).toContain("Loading");
    expect(await screen.findByRole("region", { name: "Current text of 42 U.S.C. 1395w-4" })).toBeTruthy();
    expect(api.getLawSection).toHaveBeenCalledWith(42, "1395w-4");

    fireEvent.click(screen.getByRole("button", { name: /^Hide current text/ }));
    fireEvent.click(screen.getByRole("button", { name: /^Show current text/ }));
    expect(api.getLawSection).toHaveBeenCalledTimes(1);
  });

  it("says when the text can't be loaded, and retries on the next expand", async () => {
    vi.mocked(api.getLawSection).mockRejectedValueOnce(new Error("down")).mockResolvedValueOnce(section);
    render(<LawChanges data={data} />);
    fireEvent.click(screen.getByRole("button", { name: /^Show current text/ }));
    expect((await screen.findByRole("alert")).textContent).toContain("couldn't be loaded");

    fireEvent.click(screen.getByRole("button", { name: /^Hide current text/ }));
    fireEvent.click(screen.getByRole("button", { name: /^Show current text/ }));
    await screen.findByText("(a) The current text of the section.");
    expect(api.getLawSection).toHaveBeenCalledTimes(2);
  });
});
