"use client";

import { useRef, useState } from "react";
import {
  AddressFields,
  addressLine,
  addressProblem,
  EMPTY_ADDRESS,
  type Address,
} from "@/components/reps/address-fields";
import { LocationAccuracy, OrEnterYourAddress, UseMyLocation } from "@/components/reps/use-my-location";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { seatsLabel } from "@/lib/districts";
import type { LocalReps } from "@/lib/local/reps";
import { lookUpReps, lookUpRepsAt, type Point } from "@/lib/scorecard";
import { localReps } from "@/lib/votes/hooks";

const NOT_FOUND = "We couldn't find representatives for that address. Check it and try again.";
const LOOKUP_FAILED = "The lookup didn't work. Please try again in a moment.";

/** The reps at a point, or null when no one represents it. */
async function repsAt(point: Point): Promise<LocalReps | null> {
  const reps = await lookUpRepsAt(point);
  return reps && reps.members.length > 0 ? reps : null;
}

/**
 * Find your representatives (#668): "Use my location", or a street, city, state and ZIP that
 * browser autofill can fill. Both look up POST /reps, which sends the point or the address on to
 * the Census geocoder; the browser keeps only the state, district and members.
 */
export function FindRepsForm() {
  const [address, setAddress] = useState<Address>(EMPTY_ADDRESS);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const streetRef = useRef<HTMLInputElement>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const problem = addressProblem(address);
    if (problem) {
      setError(problem);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const reps = await lookUpReps(addressLine(address));
      if (!reps || reps.members.length === 0) {
        setError(NOT_FOUND);
        return;
      }
      localReps().saveReps(reps);
    } catch {
      setError(LOOKUP_FAILED);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-lg">Find your representatives</CardTitle>
        <CardDescription>
          Use your location or your home address to find your House member and senators.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <UseMyLocation lookUp={repsAt} onFail={() => streetRef.current?.focus()}>
          {({ result, accuracy, clear }) => (
            <LocationResult
              reps={result}
              accuracy={accuracy}
              onUse={() => localReps().saveReps(result)}
              onClear={clear}
            />
          )}
        </UseMyLocation>

        <OrEnterYourAddress />

        <form onSubmit={handleSubmit} className="space-y-3" aria-label="Home address">
          <AddressFields value={address} onChange={setAddress} streetRef={streetRef} />

          <Button type="submit" className="w-full sm:w-auto" disabled={busy}>
            {busy ? "Looking up…" : "Find my representatives"}
          </Button>
          <p className="text-xs text-muted-foreground">
            Your address is used for this one lookup (through the U.S. Census Bureau&apos;s geocoder) and
            isn&apos;t saved in our database. This device keeps only your state, district and representatives.
          </p>
          <div role="alert">{error && <p className="text-sm text-destructive">{error}</p>}</div>
        </form>
      </CardContent>
    </Card>
  );
}

/**
 * What "Use my location" found, with how sure the browser was. It's saved only when the visitor
 * says so, since a rough location can land on the wrong side of a district line.
 */
function LocationResult({
  reps,
  accuracy,
  onUse,
  onClear,
}: {
  reps: LocalReps;
  accuracy: number;
  onUse: () => void;
  onClear: () => void;
}) {
  const seat =
    reps.district === null
      ? reps.state
      : `${reps.state} · ${seatsLabel([{ state: reps.state, district: reps.district }])}`;
  return (
    <div className="mt-2 space-y-2 rounded-lg border border-border p-3 text-sm">
      <p className="font-medium text-foreground">{seat}</p>
      <ul className="text-foreground">
        {reps.members.map((m) => (
          <li key={m.id}>
            {m.name} <span className="text-muted-foreground">({m.chamber})</span>
          </li>
        ))}
      </ul>
      <LocationAccuracy accuracy={accuracy} />
      <div className="flex flex-wrap gap-2">
        <Button type="button" size="sm" onClick={onUse}>
          Use these representatives
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={onClear}>
          Clear
        </Button>
      </div>
    </div>
  );
}
