// @vitest-environment jsdom
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Alert, type AlertVariant } from "@/components/ui/alert";
import { axeViolations } from "@/test/axe";

// #955: one primitive for errors and confirmations, after USWDS's alert guidance.

afterEach(cleanup);

const VARIANTS: [AlertVariant, "alert" | "status", string][] = [
  ["error", "alert", "Error"],
  ["success", "status", "Success"],
  ["info", "status", "Note"],
];

describe("Alert", () => {
  it.each(VARIANTS)("%s is a %s region whose icon names it, not just its color", async (variant, role, label) => {
    const { container } = render(<Alert variant={variant}>Your vote was saved.</Alert>);
    const region = screen.getByRole(role);
    expect(region.textContent).toBe("Your vote was saved.");
    expect(within(region).getByRole("img", { name: label })).toBeTruthy();
    expect(await axeViolations(container)).toEqual([]);
  });

  it("is a status note by default", () => {
    render(<Alert>Saved.</Alert>);
    expect(screen.getByRole("status").textContent).toBe("Saved.");
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByRole("img", { name: "Note" })).toBeTruthy();
  });

  it("shows a title above the message", () => {
    render(
      <Alert variant="error" title="Your vote wasn't saved">
        Try again.
      </Alert>,
    );
    const region = screen.getByRole("alert");
    expect(within(region).getByText("Your vote wasn't saved").tagName).toBe("P");
    expect(region.textContent).toBe("Your vote wasn't saved" + "Try again.");
  });

  it("shows a title alone", () => {
    render(<Alert variant="success" title="Saved" />);
    expect(screen.getByRole("status").textContent).toBe("Saved");
  });

  // Review Focus 2 of #951: a vote fails, and the error must be announced the first time. That needs
  // the live region in the page before its text arrives, and the same element when it fills.
  it("keeps the same live region in the page from empty to filled and back", () => {
    const { rerender } = render(<Alert variant="error">{null}</Alert>);
    const region = screen.getByRole("alert");
    expect(region.textContent).toBe("");
    // Empty, it draws nothing: no icon, no box, and it's out of the layout.
    expect(within(region).queryByRole("img")).toBeNull();
    expect(region.className).toBe("sr-only");

    rerender(<Alert variant="error">Your Yea vote couldn&apos;t be saved.</Alert>);
    expect(screen.getByRole("alert")).toBe(region);
    expect(region.textContent).toBe("Your Yea vote couldn't be saved.");
    expect(within(region).getByRole("img", { name: "Error" })).toBeTruthy();

    rerender(<Alert variant="error">{false}</Alert>);
    expect(screen.getByRole("alert")).toBe(region);
    expect(region.textContent).toBe("");
  });

  it.each([
    ["null", null],
    ["false", false],
    ["an empty string", ""],
    ["undefined", undefined],
  ])("treats %s as empty", (_, children) => {
    render(<Alert variant="error">{children}</Alert>);
    expect(screen.getByRole("alert").textContent).toBe("");
    expect(screen.queryByRole("img")).toBeNull();
  });

  it("puts the caller's classes on the filled box only", () => {
    const { rerender } = render(<Alert variant="error" className="mt-4" />);
    expect(screen.getByRole("alert").className).not.toContain("mt-4");
    rerender(
      <Alert variant="error" className="mt-4">
        Failed.
      </Alert>,
    );
    expect(screen.getByRole("alert").className).toContain("mt-4");
  });
});
