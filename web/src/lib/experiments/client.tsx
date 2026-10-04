"use client";

// The browser side of an experiment (docs/design/580-ab-experiments.md, option 3A): each arm's page
// renders <ExperimentExposure id="…" />, and the goal action calls trackConversion("…"). Each event
// goes once per visitor and experiment, to our own POST /api/experiments, which reads the arm from
// the jab_exp cookie. Nothing is sent outside Vercel production, from browsers sending Global
// Privacy Control, or from automated browsers, nor outside the experiment's window: the control page
// renders the exposure before the start too, and a note written then would keep the visitor's real
// exposure from ever being sent.

import { useEffect } from "react";
import { EXPERIMENTS, findExperiment, isLive, type Experiment } from "./registry";

/** The localStorage key of the note that each event went once. Listed on the Privacy page. */
export const NOTE_KEY = "jab.exp.v1";

/** Where the beacons go. */
export const EVENTS_URL = "/api/experiments";

type EventName = "exposure" | "conversion";

interface Note {
  experiment: string;
  exposure: boolean;
  conversion: boolean;
}

/** Whether this browser takes part in counting: production only, and never with GPC or webdriver. */
export function countingEnabled(): boolean {
  if (process.env.NEXT_PUBLIC_VERCEL_ENV !== "production") return false;
  if (typeof navigator === "undefined") return false;
  const nav = navigator as Navigator & { globalPrivacyControl?: boolean };
  return nav.globalPrivacyControl !== true && !nav.webdriver;
}

function readNote(id: string): Note {
  try {
    const parsed = JSON.parse(localStorage.getItem(NOTE_KEY) ?? "null") as Partial<Note> | null;
    if (parsed?.experiment === id) {
      return { experiment: id, exposure: parsed.exposure === true, conversion: parsed.conversion === true };
    }
  } catch {
    // A missing, unreadable or foreign note starts over for this experiment.
  }
  return { experiment: id, exposure: false, conversion: false };
}

function writeNote(note: Note): void {
  try {
    localStorage.setItem(NOTE_KEY, JSON.stringify(note));
  } catch {
    // Storage full or blocked: the event may be sent again on a later visit, which beats losing it.
  }
}

function beacon(id: string, event: EventName): boolean {
  const body = JSON.stringify({ experiment: id, event });
  try {
    if (typeof navigator.sendBeacon === "function") return navigator.sendBeacon(EVENTS_URL, body);
    void fetch(EVENTS_URL, { method: "POST", body, keepalive: true }).catch(() => undefined);
    return true;
  } catch {
    return false;
  }
}

/**
 * Sends `event` for experiment `id` unless this browser already did. A conversion goes only after
 * this browser's exposure for the same experiment, so conversions never outnumber exposures. Only
 * an experiment in the registry, inside its window by this browser's clock, is counted. Returns
 * whether it sent anything.
 */
export function sendOnce(
  id: string,
  event: EventName,
  experiments: readonly Experiment[] = EXPERIMENTS,
  now: Date = new Date()
): boolean {
  if (!countingEnabled()) return false;
  const experiment = findExperiment(id, experiments);
  if (!experiment || !isLive(experiment, now)) return false;
  const note = readNote(id);
  if (note[event]) return false;
  if (event === "conversion" && !note.exposure) return false;
  if (!beacon(id, event)) return false;
  writeNote({ ...note, [event]: true });
  return true;
}

/** Counts the goal action of experiment `id` for this visitor, once. Call it where the action happens. */
export function trackConversion(id: string): void {
  sendOnce(id, "conversion");
}

/** Counts this visitor as exposed to experiment `id`, once, after the page has loaded. Renders nothing. */
export function ExperimentExposure({ id }: { id: string }) {
  useEffect(() => {
    sendOnce(id, "exposure");
  }, [id]);
  return null;
}
