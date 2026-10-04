// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { axeViolations } from "@/test/axe";
import type { BillText, BillTextSection } from "@/lib/types";

// The Text tab's reader (#872): the parsed sections with a contents list, the raw text when the
// sections didn't parse, and an honest empty state when there's no text.

const api = vi.hoisted(() => ({ getBillText: vi.fn() }));
vi.mock("@/lib/api", async (importOriginal) => ({ ...(await importOriginal<object>()), ...api }));

const { ApiError } = await import("@/lib/api");
const { BillTextReader } = await import("../bill/bill-text-reader");
const { Sheet, SheetContent, SheetTitle } = await import("../ui/sheet");

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
  vi.unstubAllGlobals();
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

  it.each([
    ["the raw text", { sections: [], content: "<bill>SECTION 1. Raw.</bill>" }, "<bill>SECTION 1. Raw.</bill>"],
    ["no text", { sections: [], content: "" }, "No text content available."],
  ])("shows no Contents button for %s", async (_name, over, shown) => {
    api.getBillText.mockResolvedValue({ ...sectioned, ...over });
    renderReader();
    expect(await screen.findByText(shown)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Contents" })).toBeNull();
  });

  it("shows no Contents button while loading or when loading failed", async () => {
    let fail: (err: unknown) => void = () => {};
    api.getBillText.mockReturnValue(new Promise((_resolve, reject) => { fail = reject; }));
    renderReader();
    expect(screen.queryByRole("button", { name: "Contents" })).toBeNull();
    fail(new ApiError(500, "Internal Server Error", "{}"));
    expect(await screen.findByText("Failed to load bill text.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Contents" })).toBeNull();
  });
});

// #883: below 1024 px the sidebar is hidden, so a Contents button above the text opens the same
// list over the text. The tests drive it as a phone reader would; jsdom applies no CSS, so what
// shows is read from the list's `hidden` class and the text's `inert`.
describe("BillTextReader's Contents button", () => {
  /** Opens the reader with the given text and waits for its Contents button. */
  async function openReader(text: BillText = sectioned) {
    api.getBillText.mockResolvedValue(text);
    const view = renderReader();
    const button = await screen.findByRole("button", { name: "Contents" });
    const list = document.getElementById(button.getAttribute("aria-controls") ?? "");
    const firstHeading = text.sections?.[0] ? `section-${text.sections[0].id}` : "";
    const textPane = view.container.querySelector(`#${firstHeading}`)?.parentElement?.parentElement;
    if (!list || !textPane) throw new Error("the reader has no contents list or text pane");
    return { button, list, textPane };
  }

  /** The contents is showing: the button says so, the list isn't hidden, and the text is inert. */
  function expectOpen(button: HTMLElement, list: HTMLElement, textPane: Element) {
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(list.classList.contains("hidden")).toBe(false);
    expect(within(list).getByRole("navigation", { name: "Contents" })).toBeTruthy();
    expect(textPane.hasAttribute("inert")).toBe(true);
  }

  function expectClosed(button: HTMLElement, list: HTMLElement, textPane: Element) {
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(list.classList.contains("hidden")).toBe(true);
    expect(textPane.hasAttribute("inert")).toBe(false);
  }

  it("starts closed, with the text readable", async () => {
    const { button, list, textPane } = await openReader();
    expectClosed(button, list, textPane);
    expect(screen.getByRole("heading", { name: "Sec. 2. Definitions" })).toBeTruthy();
  });

  it("opens the contents over the text, which goes inert", async () => {
    const { button, list, textPane } = await openReader();
    fireEvent.click(button);
    expectOpen(button, list, textPane);
  });

  it("closes the list on choosing an entry, scrolls to the section and focuses its heading", async () => {
    const { button, list, textPane } = await openReader();
    fireEvent.click(button);
    fireEvent.click(within(list).getByRole("button", { name: "Sec. 2. Definitions" }));

    expectClosed(button, list, textPane);
    const target = document.getElementById("section-s2");
    expect(vi.mocked(Element.prototype.scrollIntoView).mock.contexts).toEqual([target]);
    expect(vi.mocked(Element.prototype.scrollIntoView).mock.calls).toEqual([[{ behavior: "smooth", block: "start" }]]);
    expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Sec. 2. Definitions" }));
  });

  it("scrolls without animation when the device asks for reduced motion", async () => {
    vi.stubGlobal("matchMedia", (query: string) =>
      new FakeMediaQueryList(query, query === "(prefers-reduced-motion: reduce)"));
    const { button, list } = await openReader();
    fireEvent.click(button);
    fireEvent.click(within(list).getByRole("button", { name: "Sec. 2. Definitions" }));
    expect(vi.mocked(Element.prototype.scrollIntoView).mock.calls).toEqual([[{ behavior: "auto", block: "start" }]]);
  });

  it("closes the list on pressing Contents again, with focus back on the button", async () => {
    const { button, list, textPane } = await openReader();
    fireEvent.click(button);
    within(list).getAllByRole("button")[0].focus();
    fireEvent.click(button);
    expectClosed(button, list, textPane);
    expect(document.activeElement).toBe(button);
    expect(Element.prototype.scrollIntoView).not.toHaveBeenCalled();
  });

  it("keeps the text pane, and so its scroll position, when the list closes without a choice", async () => {
    const { button, textPane } = await openReader();
    fireEvent.click(button);
    fireEvent.click(button);
    const after = document.getElementById("section-s2")?.parentElement?.parentElement;
    expect(after).toBe(textPane);
    expect(textPane.isConnected).toBe(true);
  });

  // The sheet closes on any Escape that reaches document: the list must take the first one. Next
  // hydrates React onto document itself, which this render can't copy: e2e/a11y.spec.ts checks it there.
  it("closes only the list on Escape inside the sheet, and the sheet on a second Escape", async () => {
    api.getBillText.mockResolvedValue(sectioned);
    const onOpenChange = vi.fn();
    render(
      <main>
        <Sheet open onOpenChange={onOpenChange}>
          <SheetContent>
            <SheetTitle>Enrolled Bill</SheetTitle>
            <BillTextReader billId="hr-119-1" versionId="ih" />
          </SheetContent>
        </Sheet>
      </main>
    );
    const button = await screen.findByRole("button", { name: "Contents" });
    fireEvent.click(button);
    const list = document.getElementById(button.getAttribute("aria-controls") ?? "");
    const entry = within(list as HTMLElement).getByRole("button", { name: "Sec. 2. Definitions" });
    entry.focus();

    fireEvent.keyDown(entry, { key: "Escape" });
    expect(onOpenChange).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: "Enrolled Bill" })).toBeTruthy();
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(document.activeElement).toBe(button);

    fireEvent.keyDown(button, { key: "Escape" });
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("closes the list when the screen widens past 1024 px, so the text isn't left inert", async () => {
    const sidebar = new FakeMediaQueryList("(min-width: 1024px)", false);
    vi.stubGlobal("matchMedia", (query: string) =>
      query === sidebar.media ? sidebar : new FakeMediaQueryList(query, false));
    const { button, list, textPane } = await openReader();
    fireEvent.click(button);
    expectOpen(button, list, textPane);

    act(() => sidebar.change(true));
    expectClosed(button, list, textPane);
  });

  it("jumps to the last of a thousand sections", async () => {
    const thousand = Array.from({ length: 1000 }, (_, i): BillTextSection => ({
      id: `s${i + 1}`,
      kind: "section",
      enum: `Sec. ${i + 1}.`,
      header: `Heading ${i + 1}`,
      content: `Text of section ${i + 1}.`,
    }));
    const { button, list } = await openReader({ ...sectioned, sections: thousand });
    fireEvent.click(button);
    // Role queries over a thousand rows take seconds in jsdom: plain selectors keep this fast.
    const entries = list.querySelectorAll("nav button");
    expect(entries).toHaveLength(1000);
    expect(entries[999].textContent).toBe("Sec. 1000. Heading 1000");

    fireEvent.click(entries[999]);
    const target = document.getElementById("section-s1000");
    expect(vi.mocked(Element.prototype.scrollIntoView).mock.contexts).toEqual([target]);
    expect(document.activeElement).toBe(target?.querySelector("h3"));
    expect(document.activeElement?.textContent).toBe("Sec. 1000. Heading 1000");
  });

  it("labels a unit with no heading by its text, keeps a long heading whole, and focuses an unheaded unit", async () => {
    const long = "Modification of authority to carry out certain prototype projects for the Department of Defense";
    const { button, list } = await openReader({
      ...sectioned,
      sections: [
        { id: "s4301", kind: "section", enum: "Sec. 4301.", header: long, content: "Amends title 10." },
        { id: "p1", kind: "paragraph", enum: "", header: "", content: "Congress finds   that libraries matter." },
      ],
    });
    fireEvent.click(button);
    const entries = within(list).getAllByRole("button").map((b) => b.textContent);
    expect(entries).toEqual([`Sec. 4301. ${long}`, "Congress finds that libraries matter."]);

    fireEvent.click(within(list).getByRole("button", { name: "Congress finds that libraries matter." }));
    expect(document.activeElement).toBe(document.getElementById("section-p1"));
  });
});

/** A MediaQueryList whose answer a test sets, and changes with change(). */
class FakeMediaQueryList extends EventTarget {
  onchange = null;

  constructor(readonly media: string, public matches: boolean) {
    super();
  }

  change(matches: boolean) {
    this.matches = matches;
    this.dispatchEvent(new Event("change"));
  }

  addListener() {}

  removeListener() {}
}
