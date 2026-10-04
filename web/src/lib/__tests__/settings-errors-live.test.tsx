// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { user as exampleUser } from "../examples";

// Review: /settings' address problems and lookup failures must reach screen readers, as
// /scorecard's FindRepsForm does (its error sits in a role="alert" region).

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  getReps: vi.fn(),
  getRepsAt: vi.fn(),
  updateMe: vi.fn(),
}));

const api = await import("../api");
const { SettingsForm } = await import("@/app/(app)/settings/settings-form");

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function submit(street: string, city: string) {
  render(<SettingsForm user={exampleUser} getIdToken={async () => "t"} onSaved={() => {}} />);
  fireEvent.change(screen.getByLabelText("Street address"), { target: { value: street } });
  fireEvent.change(screen.getByLabelText("City"), { target: { value: city } });
  fireEvent.click(screen.getByRole("button", { name: "Save district" }));
}

describe("settings form errors", () => {
  it("announces a missing ZIP (or city and state)", async () => {
    submit("1 Main St", "Springfield");
    const text = await screen.findByText("Add your ZIP code, or your city and state.");
    expect(text.closest('[role="alert"],[role="status"],[aria-live]')).not.toBeNull();
  });

  it("announces a failed lookup", async () => {
    vi.mocked(api.getReps).mockRejectedValue(new Error("502"));
    render(<SettingsForm user={exampleUser} getIdToken={async () => "t"} onSaved={() => {}} />);
    fireEvent.change(screen.getByLabelText("Street address"), { target: { value: "1 Main St" } });
    fireEvent.change(screen.getByLabelText("ZIP code"), { target: { value: "62701" } });
    fireEvent.click(screen.getByRole("button", { name: "Save district" }));
    const text = await screen.findByText("Could not find representatives for this address.");
    expect(text.closest('[role="alert"],[role="status"],[aria-live]')).not.toBeNull();
  });
});
