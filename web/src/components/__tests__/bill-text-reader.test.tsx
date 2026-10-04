// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { axeViolations } from "@/test/axe";
import type { BillText } from "@/lib/types";

// The Text tab's reader (#872): the parsed sections with a contents list, the raw text when the
// sections didn't parse, and an honest empty state when there's no text.

const api = vi.hoisted(() => ({ getBillText: vi.fn() }));
vi.mock("@/lib/api", async (importOriginal) => ({ ...(await importOriginal<object>()), ...api }));

const { ApiError } = await import("@/lib/api");
const { BillTextReader } = await import("../bill/bill-text-reader");

const sectioned: BillText = {
  id: "hr-119-1-ih",
  text_version_id: "ih",
  format: "xml",
  content_hash: "abc",
  sections: [
    {
      id: "t1",
      kind: "title",
      enum: "Title I",
      header: "Agriculture",
      content: "",
      children: [
        {
          id: "s101",
          kind: "section",
          enum: "Sec. 101.",
          header: "Short title",
          content: "This Act may be cited as the Example Act.",
          children: [{ id: "s101a", kind: "subsection", enum: "(a)", header: "In general", content: "Shall apply." }],
        },
      ],
    },
    { id: "s2", kind: "section", enum: "Sec. 2.", header: "Definitions", content: "In this Act, words mean things." },
  ],
};

/** Renders the reader inside a page's own main landmark, as the bill page's sheet does. */
function renderReader() {
  return render(
    <main>
      <BillTextReader billId="hr-119-1" versionId="ih" />
    </main>
  );
}

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn();
});

afterEach(() => {
  cleanup();
  api.getBillText.mockReset();
  delete (Element.prototype as Partial<Element>).scrollIntoView;
});

describe("BillTextReader", () => {
  it("asks for the version it was given", async () => {
    api.getBillText.mockResolvedValue(sectioned);
    renderReader();
    await screen.findByRole("heading", { name: "Title I—Agriculture" });
    expect(api.getBillText).toHaveBeenCalledWith("hr-119-1", "ih");
  });

  it("prints each section's heading at its depth and its text", async () => {
    api.getBillText.mockResolvedValue(sectioned);
    renderReader();
    expect(await screen.findByRole("heading", { level: 3, name: "Title I—Agriculture" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 4, name: "Sec. 101. Short title" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 5, name: "(a) In general" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 3, name: "Sec. 2. Definitions" })).toBeTruthy();
    expect(screen.getByText("This Act may be cited as the Example Act.")).toBeTruthy();
    expect(screen.getByText("Shall apply.")).toBeTruthy();
  });

  it("lists titles and sections in the contents, but not a section's subsections", async () => {
    api.getBillText.mockResolvedValue(sectioned);
    renderReader();
    const contents = await screen.findByRole("navigation", { name: "Contents" });
    const entries = within(contents).getAllByRole("button").map((b) => b.textContent);
    expect(entries).toEqual(["Title I—Agriculture", "Sec. 101. Short title", "Sec. 2. Definitions"]);
  });

  it("scrolls to a section chosen in the contents and marks it", async () => {
    api.getBillText.mockResolvedValue(sectioned);
    const { container } = renderReader();
    const contents = await screen.findByRole("navigation", { name: "Contents" });
    const entry = within(contents).getByRole("button", { name: "Sec. 2. Definitions" });

    fireEvent.click(entry);

    const target = container.querySelector("#section-s2");
    expect(vi.mocked(Element.prototype.scrollIntoView).mock.contexts).toEqual([target]);
    expect(entry.className).toContain("font-medium");
    expect(target?.className).toContain("bg-muted/40");
    const other = within(contents).getByRole("button", { name: "Title I—Agriculture" });
    expect(other.className).not.toContain("font-medium");
  });

  it("adds no second main landmark to the page", async () => {
    api.getBillText.mockResolvedValue(sectioned);
    renderReader();
    await screen.findByRole("heading", { name: "Sec. 2. Definitions" });
    expect(screen.getAllByRole("main")).toHaveLength(1);
  });

  it("has no axe violations", async () => {
    api.getBillText.mockResolvedValue(sectioned);
    const { container } = renderReader();
    await screen.findByRole("navigation", { name: "Contents" });
    expect(await axeViolations(container)).toEqual([]);
  });

  it("shows the raw text when the sections didn't parse", async () => {
    api.getBillText.mockResolvedValue({ ...sectioned, sections: [], content: "<bill>SECTION 1. Raw.</bill>" });
    renderReader();
    expect(await screen.findByText("<bill>SECTION 1. Raw.</bill>")).toBeTruthy();
    expect(screen.queryByRole("navigation")).toBeNull();
  });

  it.each([
    ["no sections and no content", { sections: undefined, content: undefined }],
    ["empty sections and blank content", { sections: [], content: "  \n" }],
  ])("says there's no text for %s", async (_name, over) => {
    api.getBillText.mockResolvedValue({ ...sectioned, ...over });
    renderReader();
    expect(await screen.findByText("No text content available.")).toBeTruthy();
  });

  it("says there's no text when the API has none for the version (404), not that loading failed", async () => {
    api.getBillText.mockRejectedValue(new ApiError(404, "Not Found", '{"error":"text content not found"}'));
    renderReader();
    expect(await screen.findByText("No text content available.")).toBeTruthy();
    expect(screen.queryByText("Failed to load bill text.")).toBeNull();
  });

  it("says loading failed on any other error", async () => {
    api.getBillText.mockRejectedValue(new ApiError(500, "Internal Server Error", "{}"));
    renderReader();
    expect(await screen.findByText("Failed to load bill text.")).toBeTruthy();
    expect(screen.queryByText("No text content available.")).toBeNull();
  });
});
