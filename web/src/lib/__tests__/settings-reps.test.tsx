// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { memberList, user as exampleUser } from "../examples";
import type { Member, RepsResponse, User } from "../types";
import { axeViolations } from "@/test/axe";

// /settings (#137, #438, #741). The account loads in the browser, so the server render calls no
// API at all. The address or the browser's location is used once, for POST /reps in the browser
// (10 lookups a minute per IP, and server renders share Vercel's egress IPs), and only the state
// and district are saved with PATCH /me. The fields and "Use my location" are /scorecard's (#668).
// The API is mocked.

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  getMe: vi.fn(),
  getReps: vi.fn(),
  getRepsAt: vi.fn(),
  updateMe: vi.fn(),
}));

const api = await import("../api");
const { default: SettingsPage } = await import("@/app/(app)/settings/page");
const { SettingsForm } = await import("@/app/(app)/settings/settings-form");

function member(first: string, last: string, id: string): Member {
  return { ...memberList.items[0], bioguide_id: id, first_name: first, last_name: last };
}

const FIELDS = { street: "1 Main St", city: "Springfield", state: "IL", zip: "62701" };
const ADDRESS = "1 Main St, Springfield, IL 62701";
const POINT = { latitude: 39.7817, longitude: -89.6501 };
const reps: RepsResponse = {
  reps: [member("Nikki", "Budzinski", "B001315")],
  senators: [member("Dick", "Durbin", "D000563"), member("Tammy", "Duckworth", "D000622")],
  districts: [{ state: "IL", district: 13 }],
};
const moved: User = { ...exampleUser, state: "IL", district: 13 };

const getIdToken = vi.fn(async () => "id-token-1");
const onSaved = vi.fn();

function renderForm(user: User = exampleUser) {
  render(<SettingsForm user={user} getIdToken={getIdToken} onSaved={onSaved} />);
}

function field(name: string): HTMLInputElement | HTMLSelectElement {
  return screen.getByLabelText(name);
}

function submit(fields: Partial<typeof FIELDS> = FIELDS) {
  fireEvent.change(field("Street address"), { target: { value: fields.street ?? "" } });
  fireEvent.change(field("City"), { target: { value: fields.city ?? "" } });
  fireEvent.change(field("State"), { target: { value: fields.state ?? "" } });
  fireEvent.change(field("ZIP code"), { target: { value: fields.zip ?? "" } });
  fireEvent.click(screen.getByRole("button", { name: "Save district" }));
}

/** Stands in for navigator.geolocation (jsdom has none): answers with POINT, or denies. */
function stubGeolocation(answer: "allow" | "deny" = "allow") {
  const getCurrentPosition = vi.fn((ok: PositionCallback, fail?: PositionErrorCallback | null) => {
    if (answer === "deny") {
      fail?.({ code: 1, message: "", PERMISSION_DENIED: 1, POSITION_UNAVAILABLE: 2, TIMEOUT: 3 });
      return;
    }
    ok({ coords: { ...POINT, accuracy: 30 }, timestamp: 0 } as GeolocationPosition);
  });
  Object.defineProperty(navigator, "geolocation", { value: { getCurrentPosition }, configurable: true });
}

/** Presses "Use my location" and returns the result it shows. */
async function locate(): Promise<HTMLElement> {
  fireEvent.click(screen.getByRole("button", { name: "Use my location" }));
  await screen.findByRole("button", { name: "Save this district" });
  return screen.getAllByRole("status")[0];
}

beforeEach(() => {
  vi.stubEnv("NEXT_PUBLIC_ACCOUNTS_ENABLED", "true");
  vi.mocked(api.getReps).mockResolvedValue(reps);
  vi.mocked(api.getRepsAt).mockResolvedValue(reps);
  vi.mocked(api.updateMe).mockResolvedValue(moved);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllEnvs();
  Reflect.deleteProperty(navigator, "geolocation");
});

describe("/settings server render", () => {
  it("calls no API: the account and the reps load in the browser", () => {
    const html = renderToStaticMarkup(SettingsPage());

    expect(api.getMe).not.toHaveBeenCalled();
    expect(api.getReps).not.toHaveBeenCalled();
    expect(api.getRepsAt).not.toHaveBeenCalled();
    expect(html).toContain("Settings");
  });
});

describe("SettingsForm", () => {
  it("shows the saved district and makes no lookup until asked", () => {
    renderForm();

    expect(screen.getByText(/Your district:/).textContent).toContain("CA-12");
    expect(api.getReps).not.toHaveBeenCalled();
  });

  it("gives each address field the autocomplete token browser autofill fills", () => {
    renderForm();

    expect(field("Street address").getAttribute("autocomplete")).toBe("address-line1");
    expect(field("City").getAttribute("autocomplete")).toBe("address-level2");
    expect(field("State").getAttribute("autocomplete")).toBe("address-level1");
    expect(field("ZIP code").getAttribute("autocomplete")).toBe("postal-code");
    // The single street-address box is gone: autofill filled only its street line.
    expect(document.querySelector('[autocomplete="street-address"]')).toBeNull();
  });

  it("looks up the fields as one line in the browser and saves only the state and district", async () => {
    renderForm();
    submit();

    expect(await screen.findByText("Your district is saved.")).toBeTruthy();
    expect(api.getReps).toHaveBeenCalledExactlyOnceWith(ADDRESS);
    expect(api.getRepsAt).not.toHaveBeenCalled();
    expect(api.updateMe).toHaveBeenCalledWith("id-token-1", { state: "IL", district: 13 });
    expect(onSaved).toHaveBeenCalledWith(moved);
    expect(screen.getByText("Nikki Budzinski")).toBeTruthy();
    expect(screen.getByText("Dick Durbin")).toBeTruthy();
    expect(screen.getByText("Tammy Duckworth")).toBeTruthy();
    // The address isn't kept, not even in the form.
    for (const name of ["Street address", "City", "State", "ZIP code"]) expect(field(name).value).toBe("");
  });

  it("asks for the ZIP or the city and state before looking anything up", async () => {
    renderForm();
    submit({ street: "1 Main St", city: "Springfield" });

    expect(await screen.findByText("Add your ZIP code, or your city and state.")).toBeTruthy();
    expect(api.getReps).not.toHaveBeenCalled();
    expect(api.updateMe).not.toHaveBeenCalled();
  });

  it("says which district the address votes in when the lines changed (#375)", async () => {
    vi.useFakeTimers({ now: new Date(2026, 9, 4, 12), toFake: ["Date"] });
    vi.mocked(api.getReps).mockResolvedValue({
      ...reps,
      election: { congress: 120, election_date: "2026-11-03", districts: [{ state: "IL", district: 15 }], changed: true },
    });
    try {
      renderForm();
      submit();

      expect((await screen.findByText("IL-15")).tagName).toBe("STRONG");
      expect(screen.getByText(/current representative holds \(IL-13\)/)).toBeTruthy();
      expect(screen.getByRole("link", { name: "vote.gov" }).getAttribute("target")).toBe("_blank");
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows no election line when the districts didn't change", async () => {
    vi.mocked(api.getReps).mockResolvedValue({
      ...reps,
      election: { congress: 120, election_date: "2026-11-03", districts: reps.districts, changed: false },
    });
    renderForm();
    submit();

    expect(await screen.findByText("Nikki Budzinski")).toBeTruthy();
    expect(screen.queryByRole("link", { name: "vote.gov" })).toBeNull();
  });

  it("says when the lookup fails, and saves nothing", async () => {
    vi.mocked(api.getReps).mockRejectedValue(new Error("429"));
    renderForm();
    submit();

    expect(await screen.findByText("Could not find representatives for this address.")).toBeTruthy();
    expect(api.updateMe).not.toHaveBeenCalled();
  });

  it("saves nothing when the address has no district", async () => {
    vi.mocked(api.getReps).mockResolvedValue({ reps: [], senators: [], districts: [] });
    renderForm();
    submit();

    expect(await screen.findByText("Could not find a congressional district for this address.")).toBeTruthy();
    expect(api.updateMe).not.toHaveBeenCalled();
  });

  it("explains the district-change limit", async () => {
    const body = JSON.stringify({
      error: "changed recently",
      code: "district_change_limited",
      next_change_after: "2026-10-29T00:00:00Z",
    });
    vi.mocked(api.updateMe).mockRejectedValue(new api.ApiError(429, "Too Many Requests", body));
    renderForm();
    submit();

    expect(await screen.findByText(/You changed your district recently\. You can change it again on/)).toBeTruthy();
    expect(onSaved).not.toHaveBeenCalled();
  });

  it("shows the reps at the browser's location, and saves their district only when asked", async () => {
    stubGeolocation();
    renderForm();
    const result = await locate();

    expect(api.getRepsAt).toHaveBeenCalledExactlyOnceWith(POINT.latitude, POINT.longitude);
    expect(api.getReps).not.toHaveBeenCalled();
    expect(result.textContent).toContain("Nikki Budzinski");
    expect(result.textContent).toContain("Dick Durbin");
    expect(result.textContent).toContain("within about 30 m");
    expect(api.updateMe).not.toHaveBeenCalled();

    fireEvent.click(within(result).getByRole("button", { name: "Save this district" }));

    expect(await screen.findByText("Your district is saved.")).toBeTruthy();
    expect(api.updateMe).toHaveBeenCalledExactlyOnceWith("id-token-1", { state: "IL", district: 13 });
    expect(onSaved).toHaveBeenCalledWith(moved);
    // The location result gives way to the saved reps, as after an address lookup.
    expect(screen.queryByRole("button", { name: "Save this district" })).toBeNull();
    expect(screen.getByText("Your Representatives")).toBeTruthy();
    expect(screen.getByText("Nikki Budzinski")).toBeTruthy();
  });

  it("clears a location result without saving it", async () => {
    stubGeolocation();
    renderForm();
    const result = await locate();

    fireEvent.click(within(result).getByRole("button", { name: "Clear" }));

    expect(screen.queryByText("Nikki Budzinski")).toBeNull();
    expect(api.updateMe).not.toHaveBeenCalled();
  });

  it("keeps the location result when saving it fails, so it can be saved again", async () => {
    vi.mocked(api.updateMe).mockRejectedValue(new api.ApiError(429, "Too Many Requests", "{}"));
    stubGeolocation();
    renderForm();
    const result = await locate();
    fireEvent.click(within(result).getByRole("button", { name: "Save this district" }));

    expect(await screen.findByText("Failed to save your district. Please try again.")).toBeTruthy();
    expect(onSaved).not.toHaveBeenCalled();
    // The result stays, so trying again needs no second lookup, and its unsaved reps aren't listed below.
    expect(screen.queryByText("Your Representatives")).toBeNull();
    vi.mocked(api.updateMe).mockResolvedValue(moved);
    fireEvent.click(within(result).getByRole("button", { name: "Save this district" }));

    expect(await screen.findByText("Your district is saved.")).toBeTruthy();
    expect(api.getRepsAt).toHaveBeenCalledOnce();
    expect(onSaved).toHaveBeenCalledWith(moved);
  });

  it("says when no district is at the location, and moves focus to the street", async () => {
    vi.mocked(api.getRepsAt).mockResolvedValue({ reps: [], senators: [], districts: [] });
    stubGeolocation();
    renderForm();
    fireEvent.click(screen.getByRole("button", { name: "Use my location" }));

    expect(
      await screen.findByText("We couldn't find representatives at your location. Enter your address instead.")
    ).toBeTruthy();
    expect(document.activeElement).toBe(field("Street address"));
    expect(api.updateMe).not.toHaveBeenCalled();
  });

  it("says when location is turned off, and looks nothing up", async () => {
    stubGeolocation("deny");
    renderForm();
    fireEvent.click(screen.getByRole("button", { name: "Use my location" }));

    expect(await screen.findByText("Location is turned off for this site. Enter your address instead.")).toBeTruthy();
    expect(api.getRepsAt).not.toHaveBeenCalled();
  });

  it("has no axe violations, empty and with a location result", async () => {
    stubGeolocation();
    const { container } = render(<SettingsForm user={exampleUser} getIdToken={getIdToken} onSaved={onSaved} />);
    expect(await axeViolations(container)).toEqual([]);
    await locate();
    expect(await axeViolations(container)).toEqual([]);
  });

  it("asks for a first district when there isn't one", () => {
    renderForm({ id: "u-2", created_at: "2026-09-29T00:00:00Z" });

    expect(screen.getByText("You haven't set your district yet.")).toBeTruthy();
  });
});
