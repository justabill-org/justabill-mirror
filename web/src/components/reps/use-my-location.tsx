"use client";

import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import type { Point } from "@/lib/scorecard";

/** Past this, in meters, a browser's location may be on the wrong side of a district line. */
export const COARSE_ACCURACY_M = 1000;

/** How long the browser may take to find the visitor, and how old a cached position may be. */
const LOCATION_TIMEOUT_MS = 15000;
const LOCATION_MAX_AGE_MS = 60000;

const LOOKUP_FAILED = "The lookup didn't work. Please try again in a moment.";

type Located<T> = { status: "idle" } | { status: "locating" } | { status: "found"; result: T; accuracy: number };

/** What "Use my location" found, for the form to show: the lookup's result and how sure the browser was. */
export interface LocationFound<T> {
  result: T;
  /** The browser's accuracy, in meters, rounded. */
  accuracy: number;
  /** Hides the result, e.g. once it's saved or the visitor says no. */
  clear: () => void;
}

function PinIcon() {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className="mr-2 h-4 w-4"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
    >
      <path d="M12 21s-7-6.2-7-11a7 7 0 0 1 14 0c0 4.8-7 11-7 11z" />
      <circle cx="12" cy="10" r="2.5" />
    </svg>
  );
}

/** The browser's position, as a promise; rejects with its GeolocationPositionError. */
function currentPosition(): Promise<GeolocationPosition> {
  return new Promise((resolve, reject) =>
    navigator.geolocation.getCurrentPosition(resolve, reject, {
      enableHighAccuracy: true,
      timeout: LOCATION_TIMEOUT_MS,
      maximumAge: LOCATION_MAX_AGE_MS,
    })
  );
}

function isPositionError(err: unknown): err is GeolocationPositionError {
  return typeof err === "object" && err !== null && "code" in err && "PERMISSION_DENIED" in err;
}

/**
 * "Use my location" (#668, #741): asks the browser for its position, looks up what's there with
 * `lookUp` (POST /reps with the point, which goes nowhere else), and shows what `children` renders
 * for the result. Nothing is saved here: the form decides, since a rough location can land on the
 * wrong side of a district line. Shared by /scorecard's FindRepsForm and the Settings form.
 */
export function UseMyLocation<T>({
  lookUp,
  onFail,
  children,
}: {
  /** The lookup at a point; null when no one represents it. */
  lookUp: (point: Point) => Promise<T | null>;
  /** Called after an error is shown, e.g. to move focus to the street field. */
  onFail: () => void;
  children: (found: LocationFound<T>) => ReactNode;
}) {
  const [located, setLocated] = useState<Located<T>>({ status: "idle" });
  const [error, setError] = useState<string | null>(null);

  const failed = (message: string) => {
    setLocated({ status: "idle" });
    setError(message);
    onFail();
  };

  const locate = async () => {
    setError(null);
    if (!("geolocation" in navigator)) {
      failed("This browser can't share your location. Enter your address instead.");
      return;
    }
    setLocated({ status: "locating" });
    let pos: GeolocationPosition;
    try {
      pos = await currentPosition();
    } catch (err) {
      failed(
        isPositionError(err) && err.code === err.PERMISSION_DENIED
          ? "Location is turned off for this site. Enter your address instead."
          : "We couldn't get your location. Enter your address instead."
      );
      return;
    }
    try {
      const result = await lookUp({ lat: pos.coords.latitude, lon: pos.coords.longitude });
      if (result === null) {
        failed("We couldn't find representatives at your location. Enter your address instead.");
        return;
      }
      setLocated({ status: "found", result, accuracy: Math.round(pos.coords.accuracy) });
    } catch {
      failed(LOOKUP_FAILED);
    }
  };

  return (
    <div className="space-y-2">
      <Button
        type="button"
        variant="outline"
        className="w-full sm:w-auto"
        onClick={locate}
        disabled={located.status === "locating"}
      >
        <PinIcon />
        {located.status === "locating" ? "Finding your location…" : "Use my location"}
      </Button>
      <p className="text-xs text-muted-foreground">
        Your browser asks you first. Only the coordinates are sent, for this one lookup, and they aren&apos;t saved.
      </p>
      <div role="alert">{error && <p className="text-sm text-destructive">{error}</p>}</div>
      <div role="status">
        {located.status === "found" &&
          children({
            result: located.result,
            accuracy: located.accuracy,
            clear: () => setLocated({ status: "idle" }),
          })}
      </div>
    </div>
  );
}

/** How sure the browser was of the location, and what to do if it's rough. */
export function LocationAccuracy({ accuracy }: { accuracy: number }) {
  return accuracy > COARSE_ACCURACY_M ? (
    <p className="text-muted-foreground">
      Your browser placed you within about {Math.round(accuracy / 1000)} km, which can be on the wrong side of a
      district line. If these don&apos;t look right, enter your address below.
    </p>
  ) : (
    <p className="text-muted-foreground">
      Found from your location (within about {accuracy} m). Near a district line? Enter your address below to be sure.
    </p>
  );
}

/** The divider between "Use my location" and the address fields. */
export function OrEnterYourAddress() {
  return (
    <div
      className="flex items-center gap-3 text-xs uppercase tracking-wide text-muted-foreground"
      aria-hidden="true"
    >
      <span className="h-px flex-1 bg-border" />
      or enter your address
      <span className="h-px flex-1 bg-border" />
    </div>
  );
}
