import { screen, waitFor, within } from "@testing-library/react";

// An `Alert` (components/ui/alert.tsx) is in the page, empty, before its message arrives (#955), so
// `findByRole("alert")` returns at once and `queryByRole("alert")` is never null. Tests read the
// regions that have text instead.

function scope(container?: HTMLElement) {
  return container ? within(container) : screen;
}

/** Every `role="alert"` region in `container` (default: the document) that has text. */
export function filledAlerts(container?: HTMLElement): HTMLElement[] {
  return scope(container)
    .queryAllByRole("alert")
    .filter((el) => el.textContent);
}

/** The one `role="alert"` region with text; throws if there are none or several. */
export function getAlert(container?: HTMLElement): HTMLElement {
  const filled = filledAlerts(container);
  if (filled.length !== 1) throw new Error(`expected one alert with text, found ${filled.length}`);
  return filled[0];
}

/** Waits for exactly one `role="alert"` region with text, and returns it. */
export function findAlert(container?: HTMLElement): Promise<HTMLElement> {
  return waitFor(() => getAlert(container));
}
