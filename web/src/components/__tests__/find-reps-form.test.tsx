// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { COARSE_ACCURACY_M } from "@/components/reps/use-my-location";
import { FindRepsForm } from "@/components/scorecard/find-reps-form";
import type { RepsResponse } from "@/lib/types";
import { localReps } from "@/lib/votes/hooks";
import { axeViolations } from "@/test/axe";

// #668: find your representatives by the browser's location (POST /reps with lat and lon, #711)
// or by a street, city, state and ZIP that browser autofill fills. No type-ahead.

vi.mock("@/lib/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api")>()),
  getReps: vi.fn(),
  getRepsAt: vi.fn(),
}));

const api = await import("@/lib/api");

const found: RepsResponse = {
  reps: [{ bioguide_id: "H000001", first_name: "Hana", last_name: "Hill" }],
  senators: [
    { bioguide_id: "S000001", first_name: "Sam", last_name: "Stone" },
    { bioguide_id: "S000002", first_name: "Sue", last_name: "Sand" },
  ],
  districts: [{ state: "CO", district: 3, source: "geocoder" }],
};
const none: RepsResponse = { reps: [], senators: [], districts: [] };

const POINT = { latitude: 39.0639, longitude: -108.5506 };

/** GeolocationPositionError's shape; jsdom has no such class. */
function positionError(code: 1 | 2 | 3) {
  return { code, message: "", PERMISSION_DENIED: 1, POSITION_UNAVAILABLE: 2, TIMEOUT: 3 };
}

/** Stands in for navigator.geolocation: answers with a position of this accuracy, or an error. */
function stubGeolocation(answer: { accuracy: number } | { error: 1 | 2 | 3 }) {
  const getCurrentPosition = vi.fn((ok: PositionCallback, fail?: PositionErrorCallback | null) => {
    if ("error" in answer) {
      fail?.(positionError(answer.error) as GeolocationPositionError);
      return;
    }
    ok({ coords: { ...POINT, accuracy: answer.accuracy }, timestamp: 0 } as GeolocationPosition);
  });
  Object.defineProperty(navigator, "geolocation", { value: { getCurrentPosition }, configurable: true });
  return getCurrentPosition;
}

function field(name: string): HTMLInputElement | HTMLSelectElement {
  return screen.getByLabelText(name);
}

function fillAddress({ street = "", city = "", state = "", zip = "" }) {
  fireEvent.change(field("Street address"), { target: { value: street } });
  fireEvent.change(field("City"), { target: { value: city } });
  fireEvent.change(field("State"), { target: { value: state } });
  fireEvent.change(field("ZIP code"), { target: { value: zip } });
  fireEvent.click(screen.getByRole("button", { name: "Find my representatives" }));
}

beforeEach(() => {
  localReps().clearReps();
  vi.mocked(api.getReps).mockResolvedValue(found);
  vi.mocked(api.getRepsAt).mockResolvedValue(found);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  Reflect.deleteProperty(navigator, "geolocation");
  localReps().clearReps();
});

describe("FindRepsForm: the address fields", () => {
  it("gives each field the autocomplete token browser autofill fills", () => {
    render(<FindRepsForm />);
    expect(field("Street address").getAttribute("autocomplete")).toBe("address-line1");
    expect(field("City").getAttribute("autocomplete")).toBe("address-level2");
    expect(field("State").getAttribute("autocomplete")).toBe("address-level1");
    expect(field("ZIP code").getAttribute("autocomplete")).toBe("postal-code");
    // No type-ahead: the street is a plain text field, not a combobox.
    expect(screen.queryByRole("combobox", { name: "Street address" })).toBeNull();
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("lets autofill pick the state by its code", () => {
    render(<FindRepsForm />);
    fireEvent.change(field("State"), { target: { value: "CO" } });
    expect((field("State") as HTMLSelectElement).selectedOptions[0].textContent).toBe("Colorado");
  });

  it("looks up the joined address once and saves the reps on this device", async () => {
    render(<FindRepsForm />);
    fillAddress({ street: "1 Main St", city: "Grand Junction", state: "CO", zip: "81501" });
    await waitFor(() => expect(localReps().getSnapshot().value?.district).toBe(3));
    expect(api.getReps).toHaveBeenCalledExactlyOnceWith("1 Main St, Grand Junction, CO 81501");
    expect(api.getRepsAt).not.toHaveBeenCalled();
    expect(JSON.stringify(localReps().getSnapshot().value)).not.toContain("Main St");
  });

  it.each([
    ["a ZIP alone", { zip: "81501" }, "1 Main St, 81501"],
    ["a city and state", { city: "Grand Junction", state: "CO" }, "1 Main St, Grand Junction, CO"],
  ])("is enough with %s", async (_, rest, want) => {
    render(<FindRepsForm />);
    fillAddress({ street: "1 Main St", ...rest });
    await waitFor(() => expect(api.getReps).toHaveBeenCalledWith(want));
  });

  it.each([
    ["nothing but the street", {}],
    ["a city without a state", { city: "Grand Junction" }],
    ["a state without a city", { state: "CO" }],
  ])("asks for the ZIP or the city and state with %s", async (_, rest) => {
    render(<FindRepsForm />);
    fillAddress({ street: "1 Main St", ...rest });
    expect(await screen.findByText("Add your ZIP code, or your city and state.")).toBeDefined();
    expect(api.getReps).not.toHaveBeenCalled();
  });

  it("says so when the address matches no one, and saves nothing", async () => {
    vi.mocked(api.getReps).mockResolvedValue(none);
    render(<FindRepsForm />);
    fillAddress({ street: "1 Main St", zip: "81501" });
    expect(await screen.findByText(/couldn't find representatives for that address/)).toBeDefined();
    expect(localReps().getSnapshot().value).toBeNull();
  });

  it("says the lookup didn't work when the API fails", async () => {
    vi.mocked(api.getReps).mockRejectedValue(new Error("429"));
    render(<FindRepsForm />);
    fillAddress({ street: "1 Main St", zip: "81501" });
    expect(await screen.findByText("The lookup didn't work. Please try again in a moment.")).toBeDefined();
  });
});

describe("FindRepsForm: use my location", () => {
  it("looks up the browser's position and shows the reps before saving them", async () => {
    const geo = stubGeolocation({ accuracy: 30 });
    render(<FindRepsForm />);
    fireEvent.click(screen.getByRole("button", { name: "Use my location" }));

    const status = screen.getByRole("status");
    await waitFor(() => expect(status.textContent).toContain("Hana Hill (House)"));
    expect(status.textContent).toContain("CO · District 3");
    expect(status.textContent).toContain("Sam Stone (Senate)");
    expect(status.textContent).toContain("within about 30 m");
    expect(geo).toHaveBeenCalledOnce();
    expect(api.getRepsAt).toHaveBeenCalledExactlyOnceWith(POINT.latitude, POINT.longitude);
    expect(api.getReps).not.toHaveBeenCalled();
    // Nothing is kept until the visitor says so.
    expect(localReps().getSnapshot().value).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Use these representatives" }));
    const saved = localReps().getSnapshot().value;
    expect(saved).toMatchObject({ state: "CO", district: 3 });
    expect(saved?.members.map((m) => m.name)).toEqual(["Hana Hill", "Sam Stone", "Sue Sand"]);
    expect(JSON.stringify(saved)).not.toContain(String(POINT.latitude));
  });

  it("warns that a rough location can be on the wrong side of a district line", async () => {
    stubGeolocation({ accuracy: COARSE_ACCURACY_M * 3 });
    render(<FindRepsForm />);
    fireEvent.click(screen.getByRole("button", { name: "Use my location" }));
    expect(await screen.findByText(/within about 3 km, which can be on the wrong side of a district line/)).toBeDefined();
  });

  it("clears the result without saving it", async () => {
    stubGeolocation({ accuracy: 30 });
    render(<FindRepsForm />);
    fireEvent.click(screen.getByRole("button", { name: "Use my location" }));
    fireEvent.click(await screen.findByRole("button", { name: "Clear" }));
    expect(screen.queryByText("Hana Hill")).toBeNull();
    expect(localReps().getSnapshot().value).toBeNull();
  });

  it.each([
    ["denied", { error: 1 } as const, "Location is turned off for this site. Enter your address instead."],
    ["unavailable", { error: 2 } as const, "We couldn't get your location. Enter your address instead."],
    ["timed out", { error: 3 } as const, "We couldn't get your location. Enter your address instead."],
  ])("when location is %s, says so and moves focus to the street", async (_, answer, message) => {
    stubGeolocation(answer);
    render(<FindRepsForm />);
    fireEvent.click(screen.getByRole("button", { name: "Use my location" }));
    expect(await screen.findByText(message)).toBeDefined();
    expect(document.activeElement).toBe(field("Street address"));
    expect(api.getRepsAt).not.toHaveBeenCalled();
  });

  it("says when the browser can't share a location", async () => {
    render(<FindRepsForm />);
    fireEvent.click(screen.getByRole("button", { name: "Use my location" }));
    expect(await screen.findByText("This browser can't share your location. Enter your address instead.")).toBeDefined();
    expect(api.getRepsAt).not.toHaveBeenCalled();
  });

  it("says when no one represents the point", async () => {
    stubGeolocation({ accuracy: 30 });
    vi.mocked(api.getRepsAt).mockResolvedValue(none);
    render(<FindRepsForm />);
    fireEvent.click(screen.getByRole("button", { name: "Use my location" }));
    expect(
      await screen.findByText("We couldn't find representatives at your location. Enter your address instead.")
    ).toBeDefined();
    expect(document.activeElement).toBe(field("Street address"));
  });

  it("says the lookup didn't work when the API fails", async () => {
    stubGeolocation({ accuracy: 30 });
    vi.mocked(api.getRepsAt).mockRejectedValue(new Error("503"));
    render(<FindRepsForm />);
    fireEvent.click(screen.getByRole("button", { name: "Use my location" }));
    expect(await screen.findByText("The lookup didn't work. Please try again in a moment.")).toBeDefined();
    expect(screen.getByRole("button", { name: "Use my location" })).toHaveProperty("disabled", false);
  });
});

describe("FindRepsForm accessibility", () => {
  it("has no axe violations, empty and with a location result", async () => {
    stubGeolocation({ accuracy: 30 });
    const { container } = render(<FindRepsForm />);
    expect(await axeViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Use my location" }));
    await screen.findByRole("button", { name: "Use these representatives" });
    expect(await axeViolations(container)).toEqual([]);
  });

  it("names the form and keeps the error regions in the page before they fill", () => {
    render(<FindRepsForm />);
    expect(screen.getByRole("form", { name: "Home address" })).toBeDefined();
    expect(screen.getAllByRole("alert")).toHaveLength(2);
    expect(screen.getByRole("status")).toBeDefined();
  });
});
