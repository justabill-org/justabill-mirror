// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../ui/tabs";
import { axeViolations } from "@/test/axe";

afterEach(cleanup);

function renderTabs(linkable?: string[]) {
  return render(
    <Tabs defaultValue="actions" linkable={linkable}>
      <TabsList aria-label="Bill details">
        <TabsTrigger value="actions">Actions</TabsTrigger>
        <TabsTrigger value="text">Text</TabsTrigger>
        <TabsTrigger value="votes">Votes</TabsTrigger>
      </TabsList>
      <TabsContent value="actions">Action list</TabsContent>
      <TabsContent value="text">Text list</TabsContent>
      <TabsContent value="votes">Vote list</TabsContent>
    </Tabs>
  );
}

const tab = (name: string) => screen.getByRole("tab", { name });

describe("Tabs", () => {
  it("puts only the selected tab in the Tab order", () => {
    renderTabs();
    expect(tab("Actions").tabIndex).toBe(0);
    expect(tab("Text").tabIndex).toBe(-1);
    expect(tab("Votes").tabIndex).toBe(-1);
  });

  it("moves focus and selection with the arrow keys, wrapping at the ends", () => {
    renderTabs();
    tab("Actions").focus();

    fireEvent.keyDown(tab("Actions"), { key: "ArrowRight" });
    expect(document.activeElement).toBe(tab("Text"));
    expect(tab("Text").getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("tabpanel").textContent).toBe("Text list");

    fireEvent.keyDown(tab("Text"), { key: "ArrowRight" });
    fireEvent.keyDown(tab("Votes"), { key: "ArrowRight" });
    expect(document.activeElement).toBe(tab("Actions"));

    fireEvent.keyDown(tab("Actions"), { key: "ArrowLeft" });
    expect(document.activeElement).toBe(tab("Votes"));
    expect(tab("Votes").tabIndex).toBe(0);
    expect(tab("Actions").tabIndex).toBe(-1);
  });

  it("jumps to the first and last tab with Home and End", () => {
    renderTabs();
    tab("Actions").focus();
    fireEvent.keyDown(tab("Actions"), { key: "End" });
    expect(document.activeElement).toBe(tab("Votes"));
    fireEvent.keyDown(tab("Votes"), { key: "Home" });
    expect(document.activeElement).toBe(tab("Actions"));
  });

  it("ignores other keys", () => {
    renderTabs();
    tab("Actions").focus();
    fireEvent.keyDown(tab("Actions"), { key: "ArrowDown" });
    expect(document.activeElement).toBe(tab("Actions"));
  });

  it("links the selected tab and its panel", () => {
    renderTabs();
    const panel = screen.getByRole("tabpanel");
    expect(tab("Actions").getAttribute("aria-controls")).toBe(panel.id);
    expect(panel.getAttribute("aria-labelledby")).toBe(tab("Actions").id);
    expect(tab("Text").hasAttribute("aria-controls")).toBe(false);
  });

  it("gives two tab sets on one page different ids", () => {
    renderTabs();
    renderTabs();
    const ids = screen.getAllByRole("tab").map((t) => t.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("has no axe violations", async () => {
    const { container } = renderTabs();
    expect(await axeViolations(container)).toEqual([]);
  });
});

// My votes links to a bill's Votes tab with /bills/<id>#votes (#843).
describe("Tabs opened by the URL fragment", () => {
  const scrollIntoView = vi.fn();

  function at(hash: string) {
    window.history.replaceState(null, "", `/bills/hr-119-1${hash}`);
    Element.prototype.scrollIntoView = scrollIntoView;
    scrollIntoView.mockClear();
  }

  afterEach(() => {
    delete (Element.prototype as Partial<Element>).scrollIntoView;
  });

  it("opens the tab the fragment names and scrolls to the tabs", () => {
    at("#votes");
    renderTabs(["votes"]);
    expect(tab("Votes").getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("tabpanel").textContent).toBe("Vote list");
    expect(scrollIntoView).toHaveBeenCalledWith({ block: "start" });

    // Picking another tab still works with the fragment in place.
    fireEvent.click(tab("Text"));
    expect(screen.getByRole("tabpanel").textContent).toBe("Text list");
  });

  it("opens the default for a tab that isn't linkable, and doesn't scroll", () => {
    at("#votes");
    renderTabs(["text"]);
    expect(screen.getByRole("tabpanel").textContent).toBe("Action list");
    expect(scrollIntoView).not.toHaveBeenCalled();
  });

  it("follows a fragment that changes on the page", () => {
    at("");
    renderTabs(["votes"]);
    expect(screen.getByRole("tabpanel").textContent).toBe("Action list");
    act(() => {
      window.history.replaceState(null, "", "/bills/hr-119-1#votes");
      window.dispatchEvent(new HashChangeEvent("hashchange"));
    });
    expect(screen.getByRole("tabpanel").textContent).toBe("Vote list");
  });
});

describe("Tabs picked by a click, then linked by the fragment", () => {
  afterEach(() => {
    delete (Element.prototype as Partial<Element>).scrollIntoView;
  });

  function changeHash(hash: string) {
    act(() => {
      window.history.replaceState(null, "", `/bills/hr-119-1${hash}`);
      window.dispatchEvent(new HashChangeEvent("hashchange"));
    });
  }

  it("follows a fragment that changes after a tab was clicked", () => {
    window.history.replaceState(null, "", "/bills/hr-119-1");
    Element.prototype.scrollIntoView = vi.fn();
    renderTabs(["votes"]);
    fireEvent.click(tab("Text"));
    changeHash("#votes");
    expect(screen.getByRole("tabpanel").textContent).toBe("Vote list");
  });

  it("keeps the clicked tab when the fragment changes to one that names no tab", () => {
    window.history.replaceState(null, "", "/bills/hr-119-1");
    Element.prototype.scrollIntoView = vi.fn();
    renderTabs(["votes"]);
    fireEvent.click(tab("Text"));
    changeHash("#footnote-1");
    expect(screen.getByRole("tabpanel").textContent).toBe("Text list");
  });
});
