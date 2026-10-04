// @vitest-environment jsdom
import { cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { countingEnabled, EVENTS_URL, ExperimentExposure, NOTE_KEY, sendOnce, trackConversion } from "../experiments/client";
import { EXPERIMENTS } from "../experiments/registry";

// A fixture registry, so these tests outlive whichever experiment is live; the clock sits inside its window.
vi.mock("../experiments/registry", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../experiments/registry")>()),
  EXPERIMENTS: [
    {
      id: "vote-deck-layout",
      issue: 1,
      path: "/vote",
      hypothesis: "h",
      goal: "g",
      baseline: 0.1,
      target: 0.12,
      samplePerArm: 3841,
      start: "2026-10-12",
      end: "2026-10-26",
    },
  ],
}));

const ID = "vote-deck-layout";
const LIVE = new Date("2026-10-15T12:00:00Z");
let sendBeacon: ReturnType<typeof vi.fn>;

function browser(extra: Record<string, unknown> = {}) {
  sendBeacon = vi.fn(() => true);
  vi.stubGlobal("navigator", { sendBeacon, webdriver: false, ...extra });
}

function sent(): unknown[] {
  return sendBeacon.mock.calls.map(([url, body]) => {
    expect(url).toBe(EVENTS_URL);
    return JSON.parse(body as string);
  });
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(LIVE);
  vi.stubEnv("NEXT_PUBLIC_VERCEL_ENV", "production");
  browser();
});

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.useRealTimers();
});

describe("experiment beacons", () => {
  it("sends one exposure and one conversion however often the visitor comes back and converts", () => {
    const first = render(<ExperimentExposure id={ID} />);
    first.unmount();
    render(<ExperimentExposure id={ID} />);
    trackConversion(ID);
    trackConversion(ID);
    expect(sent()).toEqual([
      { experiment: ID, event: "exposure" },
      { experiment: ID, event: "conversion" },
    ]);
    expect(JSON.parse(localStorage.getItem(NOTE_KEY) ?? "")).toEqual({
      experiment: ID,
      exposure: true,
      conversion: true,
    });
  });

  it("sends no conversion before an exposure", () => {
    expect(sendOnce(ID, "conversion")).toBe(false);
    expect(sendBeacon).not.toHaveBeenCalled();
  });

  it("starts over for a new experiment", () => {
    localStorage.setItem(NOTE_KEY, JSON.stringify({ experiment: "old-test", exposure: true, conversion: true }));
    expect(sendOnce(ID, "exposure")).toBe(true);
    expect(JSON.parse(localStorage.getItem(NOTE_KEY) ?? "").experiment).toBe(ID);
  });

  it("treats an unreadable note as none", () => {
    localStorage.setItem(NOTE_KEY, "{not json");
    expect(sendOnce(ID, "exposure")).toBe(true);
  });

  it("tries again later when the browser refuses the beacon", () => {
    sendBeacon.mockReturnValueOnce(false);
    expect(sendOnce(ID, "exposure")).toBe(false);
    expect(localStorage.getItem(NOTE_KEY)).toBeNull();
    expect(sendOnce(ID, "exposure")).toBe(true);
  });

  it("falls back to a keepalive fetch without sendBeacon", () => {
    const fetchMock = vi.fn(() => Promise.resolve(new Response(null, { status: 204 })));
    vi.stubGlobal("navigator", { webdriver: false });
    vi.stubGlobal("fetch", fetchMock);
    expect(sendOnce(ID, "exposure")).toBe(true);
    expect(fetchMock).toHaveBeenCalledWith(EVENTS_URL, {
      method: "POST",
      body: JSON.stringify({ experiment: ID, event: "exposure" }),
      keepalive: true,
    });
  });

  // The control page renders the exposure before the start too: a note written then would keep the
  // visitor's exposure from being sent once the experiment runs.
  it("sends nothing outside the experiment's window, or for an experiment not in the registry", () => {
    expect(sendOnce(ID, "exposure", EXPERIMENTS, new Date("2026-10-11T23:59:59Z"))).toBe(false);
    expect(sendOnce(ID, "exposure", EXPERIMENTS, new Date("2026-10-26T00:00:00Z"))).toBe(false);
    expect(sendOnce("not-registered", "exposure")).toBe(false);
    expect(sendBeacon).not.toHaveBeenCalled();
    expect(localStorage.getItem(NOTE_KEY)).toBeNull();
    expect(sendOnce(ID, "exposure", EXPERIMENTS, new Date("2026-10-12T00:00:00Z"))).toBe(true);
  });

  it.each([
    ["outside production", () => vi.stubEnv("NEXT_PUBLIC_VERCEL_ENV", "preview")],
    ["with Global Privacy Control", () => browser({ globalPrivacyControl: true })],
    ["under webdriver", () => browser({ webdriver: true })],
  ])("sends nothing %s", (_, arrange) => {
    arrange();
    render(<ExperimentExposure id={ID} />);
    trackConversion(ID);
    expect(countingEnabled()).toBe(false);
    expect(sendBeacon).not.toHaveBeenCalled();
    expect(localStorage.getItem(NOTE_KEY)).toBeNull();
  });
});
