// @vitest-environment jsdom
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Sheet, SheetClose, SheetContent, SheetHeader, SheetTitle } from "../ui/sheet";
import { axeViolations } from "@/test/axe";

afterEach(cleanup);

function Harness() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Details
      </button>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent>
          <SheetHeader>
            <SheetTitle>H.R. 1: A bill</SheetTitle>
            <SheetClose onClick={() => setOpen(false)} />
          </SheetHeader>
          <a href="https://www.congress.gov/">Congress.gov</a>
          <button type="button" onClick={() => setOpen(false)}>
            Back to voting
          </button>
        </SheetContent>
      </Sheet>
    </>
  );
}

function openSheet() {
  render(<Harness />);
  const opener = screen.getByRole("button", { name: "Details" });
  opener.focus();
  fireEvent.click(opener);
  return opener;
}

describe("Sheet", () => {
  it("is a modal dialog named by its title", () => {
    openSheet();
    const dialog = screen.getByRole("dialog", { name: "H.R. 1: A bill" });
    expect(dialog.getAttribute("aria-modal")).toBe("true");
  });

  it("moves focus into the dialog when it opens", () => {
    openSheet();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Close" }));
  });

  it("keeps Tab and Shift+Tab inside the dialog", () => {
    openSheet();
    const close = screen.getByRole("button", { name: "Close" });
    const back = screen.getByRole("button", { name: "Back to voting" });

    back.focus();
    fireEvent.keyDown(document, { key: "Tab" });
    expect(document.activeElement).toBe(close);

    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(back);
  });

  it("skips controls taken out of the tab order with tabindex -1", () => {
    render(
      <Sheet open onOpenChange={() => {}}>
        <SheetContent>
          <SheetTitle>Tabs</SheetTitle>
          <button type="button">Active tab</button>
          <button type="button" tabIndex={-1}>
            Inactive tab
          </button>
          <a href="https://www.congress.gov/" tabIndex={-1}>
            Skipped link
          </a>
        </SheetContent>
      </Sheet>
    );
    const active = screen.getByRole("button", { name: "Active tab" });
    active.focus();
    fireEvent.keyDown(document, { key: "Tab" });
    expect(document.activeElement).toBe(active);
    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(active);
  });

  // #883: below 1024 px the bill text reader's contents is display: none until opened, and the
  // text behind the open contents is inert. Tab past the last control a person can reach must
  // wrap, not leave the dialog through one they can't.
  it("wraps past controls that aren't rendered or are inert", () => {
    render(
      <Sheet open onOpenChange={() => {}}>
        <SheetContent>
          <SheetTitle>Reader</SheetTitle>
          <button type="button">Contents</button>
          <button type="button">Hidden entry</button>
          <div inert>
            <a href="https://www.congress.gov/">Inert link</a>
          </div>
        </SheetContent>
      </Sheet>
    );
    const hidden = screen.getByRole("button", { name: "Hidden entry" });
    hidden.checkVisibility = () => false; // jsdom has no layout: say what the browser would.
    const contents = screen.getByRole("button", { name: "Contents" });
    contents.focus();
    fireEvent.keyDown(document, { key: "Tab" });
    expect(document.activeElement).toBe(contents);
    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(contents);
  });

  it("pulls focus back in if it escaped the dialog", () => {
    openSheet();
    document.body.focus();
    fireEvent.keyDown(document, { key: "Tab" });
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Close" }));
  });

  it("closes on Escape and returns focus to the opener", () => {
    const opener = openSheet();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement).toBe(opener);
  });

  it("focuses the dialog itself when it has nothing focusable", () => {
    render(
      <Sheet open onOpenChange={() => {}}>
        <SheetContent>
          <SheetTitle>Empty</SheetTitle>
        </SheetContent>
      </Sheet>
    );
    const dialog = screen.getByRole("dialog");
    expect(document.activeElement).toBe(dialog);
    fireEvent.keyDown(document, { key: "Tab" });
    expect(document.activeElement).toBe(dialog);
  });

  it("has no axe violations when open", async () => {
    openSheet();
    expect(await axeViolations(document.body)).toEqual([]);
  });
});
