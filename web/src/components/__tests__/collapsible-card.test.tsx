// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { CollapsibleCard } from "../ui/collapsible-card";
import { axeViolations } from "@/test/axe";

afterEach(cleanup);

/** Stubs matchMedia so the lg query matches (a wide screen) or not. */
function stubWidth(wide: boolean) {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: wide,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
}

function body() {
  return screen.getByText("Card body").parentElement as HTMLElement;
}

describe("CollapsibleCard", () => {
  it("starts closed on a narrow screen, with the summary line, and opens on a tap", () => {
    stubWidth(false);
    render(
      <CollapsibleCard title="Related bills" summary="2 related bills">
        <p>Card body</p>
      </CollapsibleCard>
    );
    const button = screen.getByRole("button", { name: /Related bills/ });
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(button.textContent).toContain("2 related bills");
    expect(body().className).toContain("max-lg:hidden");
    expect(screen.getByRole("heading", { level: 3 }).contains(button)).toBe(true);

    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(button.textContent).not.toContain("2 related bills");
    expect(body().className).not.toContain("hidden");

    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(body().className).toMatch(/(^|\s)hidden(\s|$)/);
  });

  it("starts open on a wide screen and closes on a click", () => {
    stubWidth(true);
    render(
      <CollapsibleCard title="Related bills" summary="2 related bills">
        <p>Card body</p>
      </CollapsibleCard>
    );
    const button = screen.getByRole("button", { name: /Related bills/ });
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(button.getAttribute("aria-controls")).toBe(body().id);

    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(body().className).toMatch(/(^|\s)hidden(\s|$)/);
  });

  it("starts open on a narrow screen with defaultOpenNarrow", () => {
    stubWidth(false);
    render(
      <CollapsibleCard title="AI Summary" defaultOpenNarrow>
        <p>Card body</p>
      </CollapsibleCard>
    );
    expect(screen.getByRole("button", { name: /AI Summary/ }).getAttribute("aria-expanded")).toBe("true");
    expect(body().className).not.toContain("hidden");
  });

  it("has no axe violations, open or closed", async () => {
    stubWidth(false);
    const { container } = render(
      <CollapsibleCard title="Related bills" summary="2 related bills">
        <p>Card body</p>
      </CollapsibleCard>
    );
    expect(await axeViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: /Related bills/ }));
    expect(await axeViolations(container)).toEqual([]);
  });
});
