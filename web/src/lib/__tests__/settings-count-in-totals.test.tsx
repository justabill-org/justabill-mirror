// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { CountInTotals } from "@/app/(app)/settings/count-in-totals";
import { axeViolations } from "@/test/axe";
import { findAlert, filledAlerts } from "@/test/alert";
import type { User } from "../types";

// "Count my votes in the published totals" on /settings (#1040): off sends PATCH /me
// {"count_in_totals": false} and the API stamps agg_opted_out_at; on sends true and clears it. A
// failed save puts the box back and says so in an alert; a saved one says when it applies.

const counted: User = {
  id: "u-1",
  state: "CA",
  district: 12,
  created_at: "2026-09-01T00:00:00Z",
  agg_opted_out_at: null,
};
const optedOut: User = { ...counted, agg_opted_out_at: "2026-10-26T12:00:00Z" };

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function renderSwitch(user: User, updateMe = vi.fn<(token: string, data: object) => Promise<User>>()) {
  const onSaved = vi.fn<(user: User) => void>();
  const { container } = render(
    <CountInTotals user={user} getIdToken={async () => "token-1"} onSaved={onSaved} api={{ updateMe }} />
  );
  const box = () => screen.getByRole("checkbox", { name: "Count my votes in the published totals" });
  return { container, box, updateMe, onSaved };
}

/** The role="status" region (the success Alert) that has text. */
function filledStatus(): HTMLElement | undefined {
  return screen.queryAllByRole("status").find((el) => el.textContent);
}

describe("count in totals", () => {
  it("is on for a new account and for an API that doesn't send the field yet, off once opted out", () => {
    const { box } = renderSwitch(counted);
    expect((box() as HTMLInputElement).checked).toBe(true);
    cleanup();

    const older: User = { ...counted };
    delete older.agg_opted_out_at;
    expect((renderSwitch(older).box() as HTMLInputElement).checked).toBe(true);
    cleanup();

    expect((renderSwitch(optedOut).box() as HTMLInputElement).checked).toBe(false);
  });

  it("says the change applies from the next update of the totals", () => {
    renderSwitch(counted);
    expect(
      screen.getByRole("checkbox", { description: /applies from the next update of the totals/ })
    ).toBeTruthy();
  });

  it("switched off, saves count_in_totals false and announces it", async () => {
    const { box, updateMe, onSaved } = renderSwitch(counted);
    updateMe.mockResolvedValue(optedOut);

    fireEvent.click(box());

    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(optedOut));
    expect(updateMe).toHaveBeenCalledTimes(1);
    expect(updateMe).toHaveBeenCalledWith("token-1", { count_in_totals: false });
    expect((box() as HTMLInputElement).checked).toBe(false);
    expect(filledStatus()?.textContent).toContain("left out of the published totals from their next update");
    expect(filledAlerts()).toEqual([]);
  });

  it("switched back on, saves count_in_totals true", async () => {
    const { box, updateMe, onSaved } = renderSwitch(optedOut);
    updateMe.mockResolvedValue(counted);

    fireEvent.click(box());

    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(counted));
    expect(updateMe).toHaveBeenCalledWith("token-1", { count_in_totals: true });
    expect((box() as HTMLInputElement).checked).toBe(true);
    expect(filledStatus()?.textContent).toContain("count in the published totals from their next update");
  });

  it("rolls back and announces a failed save", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const { container, box, updateMe, onSaved } = renderSwitch(counted);
    updateMe.mockRejectedValue(new Error("503"));

    fireEvent.click(box());

    const alert = await findAlert();
    expect(alert.textContent).toContain("We couldn't save this setting");
    expect((box() as HTMLInputElement).checked).toBe(true);
    expect(onSaved).not.toHaveBeenCalled();
    expect(filledStatus()).toBeUndefined();
    expect(await axeViolations(container)).toEqual([]);
  });

  it("ignores a second click while the first save is pending", async () => {
    let resolve: (u: User) => void = () => {};
    const { box, updateMe } = renderSwitch(counted);
    updateMe.mockImplementation(() => new Promise<User>((r) => (resolve = r)));

    fireEvent.click(box());
    await waitFor(() => expect(updateMe).toHaveBeenCalledTimes(1));
    fireEvent.click(box());
    resolve(optedOut);

    await waitFor(() => expect(filledStatus()).toBeDefined());
    expect(updateMe).toHaveBeenCalledTimes(1);
    expect((box() as HTMLInputElement).checked).toBe(false);
  });

  it("has no axe violations", async () => {
    const { container } = renderSwitch(counted);
    expect(within(container).getByRole("heading", { name: "Published totals" })).toBeTruthy();
    expect(await axeViolations(container)).toEqual([]);
  });
});
