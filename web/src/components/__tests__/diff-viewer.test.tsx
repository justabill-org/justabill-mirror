// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { DiffCard } from "../diff/diff-viewer";
import * as api from "@/lib/api";
import type { BillTextDiff, DiffDetailResponse } from "@/lib/types";

vi.mock("@/lib/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api")>()),
  getDiff: vi.fn(),
}));
const getDiff = vi.mocked(api.getDiff);

// What GET /bills/{id} carries since #450: the diff's metadata, no section changes.
const meta: BillTextDiff = {
  id: "d1",
  bill_id: "hr-119-1",
  from_version_id: "v1",
  to_version_id: "v2",
  diff_stats: { sections_added: 1, sections_removed: 0, sections_modified: 0, words_added: 12, words_removed: 0 },
};

const detail: DiffDetailResponse = {
  diff: {
    ...meta,
    diff_content: [{ section_id: "s2", header: "Sec. 2. New rule", type: "added", new_text: "A new rule." }],
  },
  summary: { diff_id: "d1", summary: "Adds a rule.", model_used: "gemini" },
};

beforeEach(() => {
  getDiff.mockReset();
});
afterEach(cleanup);

/** One DiffCard with its own expanded state, as the Text tab's Versions list holds it. */
function Harness({ diff = meta }: { diff?: BillTextDiff }) {
  const [open, setOpen] = useState(false);
  return <DiffCard diff={diff} billId="hr-119-1" isExpanded={open} onToggle={() => setOpen(!open)} />;
}

describe("DiffCard", () => {
  it("loads a diff's changes only when it's expanded", async () => {
    getDiff.mockResolvedValue(detail);
    render(<Harness />);

    expect(screen.getByText("+1 sections")).toBeTruthy();
    expect(getDiff).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: /Version Comparison/ }));
    expect(screen.getByRole("status", { name: "Loading changes" })).toBeTruthy();

    expect(await screen.findByText("Sec. 2. New rule")).toBeTruthy();
    expect(screen.getByText("A new rule.")).toBeTruthy();
    expect(screen.getByText("Adds a rule.")).toBeTruthy();
    // The AI summary says it's AI next to the text, not only in a footer.
    expect(screen.getByText(/AI-generated from the two versions' text/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "How summaries are made" }).getAttribute("href")).toBe(
      "/methodology#summaries",
    );
    expect(getDiff).toHaveBeenCalledExactlyOnceWith("hr-119-1", "d1");

    // Collapsing and expanding again reuses what was loaded.
    fireEvent.click(screen.getByRole("button", { name: /Version Comparison/ }));
    fireEvent.click(screen.getByRole("button", { name: /Version Comparison/ }));
    expect(screen.getByText("A new rule.")).toBeTruthy();
    expect(getDiff).toHaveBeenCalledTimes(1);
  });

  it("says so when the changes fail to load", async () => {
    getDiff.mockRejectedValue(new Error("down"));
    render(<Harness />);

    fireEvent.click(screen.getByRole("button", { name: /Version Comparison/ }));

    expect(await screen.findByText("Failed to load the changes.")).toBeTruthy();
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("shows no detailed changes for a diff without section changes", async () => {
    getDiff.mockResolvedValue({ diff: meta, summary: null });
    render(<Harness />);

    fireEvent.click(screen.getByRole("button", { name: /Version Comparison/ }));

    expect(await screen.findByText("No detailed changes available.")).toBeTruthy();
  });

  it("reports its state on the toggle", () => {
    getDiff.mockResolvedValue(detail);
    render(<Harness />);
    const toggle = screen.getByRole("button", { name: /Version Comparison/ });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
  });

  it("says a diff with nothing changed has no changes to the text", () => {
    const zero = { sections_added: 0, sections_removed: 0, sections_modified: 0, words_added: 0, words_removed: 0 };
    render(<Harness diff={{ ...meta, diff_stats: zero }} />);
    expect(screen.getByText("No changes to the text")).toBeTruthy();
  });
});
