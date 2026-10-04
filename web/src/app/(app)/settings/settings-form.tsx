"use client";

import { useRef, useState } from "react";
import type { User, RepsResponse } from "@/lib/types";
import { ApiError, getReps, getRepsAt, updateMe } from "@/lib/api";
import { districtName, noSenatorsNote, seatsLabel } from "@/lib/districts";
import type { Point } from "@/lib/scorecard";
import { ElectionDistrictNote } from "@/components/member/election-district-note";
import {
  AddressFields,
  addressLine,
  addressProblem,
  EMPTY_ADDRESS,
  type Address,
} from "@/components/reps/address-fields";
import { LocationAccuracy, OrEnterYourAddress, UseMyLocation } from "@/components/reps/use-my-location";
import { Button } from "@/components/ui/button";

interface SettingsFormProps {
  user: User;
  /** A current ID token for PATCH /me (useUser().getIdToken). */
  getIdToken: () => Promise<string>;
  /** Called with the account the API answered with after a save. */
  onSaved: (user: User) => void;
}

/** The message for a PATCH /me that failed, e.g. the district-change limit (429). */
function saveErrorMessage(err: unknown): string {
  if (err instanceof ApiError && err.code === "district_change_limited") {
    try {
      const { next_change_after: after } = JSON.parse(err.body) as { next_change_after?: string };
      const when = after ? new Date(after) : null;
      if (when && !Number.isNaN(when.getTime())) {
        return `You changed your district recently. You can change it again on ${when.toLocaleDateString()}.`;
      }
    } catch {
      // Fall through to the general message.
    }
    return "You changed your district recently. Try again later.";
  }
  return "Failed to save your district. Please try again.";
}

/** The reps at a point, or null when it's in no congressional district. */
async function repsAt({ lat, lon }: Point): Promise<RepsResponse | null> {
  const found = await getRepsAt(lat, lon);
  return found.districts.length > 0 ? found : null;
}

/**
 * The account's state and district, from "Use my location" or a street, city, state and ZIP that
 * browser autofill fills (#741, the fields and button /scorecard uses, #668). The point or the
 * address is used once, for the Census lookup in the browser (POST /reps, #438, #711), and only
 * the state and district are saved: our servers never keep a street address or a location (#55).
 */
export function SettingsForm({ user, getIdToken, onSaved }: SettingsFormProps) {
  const [address, setAddress] = useState<Address>(EMPTY_ADDRESS);
  const [isSaving, setIsSaving] = useState(false);
  const [message, setMessage] = useState<{ ok: boolean; text: string } | null>(null);
  const [reps, setReps] = useState<RepsResponse | null>(null);
  const [repsError, setRepsError] = useState("");
  const streetRef = useRef<HTMLInputElement>(null);

  const current =
    user.state && user.district !== undefined ? districtName(user.state, user.district) : null;

  const startSave = () => {
    setIsSaving(true);
    setMessage(null);
    setRepsError("");
  };

  /** Saves a lookup's state and district with PATCH /me; true once saved. */
  const saveDistrict = async (found: RepsResponse): Promise<boolean> => {
    const district = found.districts[0];
    if (!district) {
      setRepsError("Could not find a congressional district for this address.");
      return false;
    }
    try {
      const saved = await updateMe(await getIdToken(), {
        state: district.state,
        district: district.district,
      });
      onSaved(saved);
      setMessage({ ok: true, text: "Your district is saved." });
      return true;
    } catch (err) {
      setMessage({ ok: false, text: saveErrorMessage(err) });
      return false;
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const problem = addressProblem(address);
    if (problem) {
      setMessage(null);
      setRepsError(problem);
      return;
    }
    startSave();
    setReps(null);
    try {
      let found: RepsResponse;
      try {
        found = await getReps(addressLine(address));
      } catch {
        setRepsError("Could not find representatives for this address.");
        return;
      }
      setReps(found);
      if (await saveDistrict(found)) setAddress(EMPTY_ADDRESS);
    } finally {
      setIsSaving(false);
    }
  };

  /**
   * Saves a location result. Only once it's saved do its reps move below and the result close: a
   * failed save keeps it, so trying again needs no second lookup.
   */
  const saveLocated = async (found: RepsResponse, clear: () => void) => {
    startSave();
    try {
      if (await saveDistrict(found)) {
        setReps(found);
        clear();
      }
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <div className="space-y-6">
      <p className="text-sm text-foreground">
        {current ? (
          <>
            Your district: <span className="font-medium">{current}</span>
          </>
        ) : (
          "You haven't set your district yet."
        )}
      </p>

      <div className="space-y-5">
        {current && <p className="text-sm text-foreground">Moved? Use your location or enter your new address.</p>}
        <UseMyLocation lookUp={repsAt} onFail={() => streetRef.current?.focus()}>
          {({ result, accuracy, clear }) => (
            <div className="mt-2 space-y-3 rounded-lg border border-border p-3 text-sm">
              <RepsList reps={result} />
              <LocationAccuracy accuracy={accuracy} />
              <div className="flex flex-wrap gap-2">
                <Button type="button" size="sm" disabled={isSaving} onClick={() => saveLocated(result, clear)}>
                  Save this district
                </Button>
                <Button type="button" variant="ghost" size="sm" onClick={clear}>
                  Clear
                </Button>
              </div>
            </div>
          )}
        </UseMyLocation>

        <OrEnterYourAddress />

        <form onSubmit={handleSubmit} className="space-y-3" aria-label="Home address">
          <AddressFields value={address} onChange={setAddress} streetRef={streetRef} />
          <p className="text-xs text-muted-foreground">
            We use it once to find your district and don&apos;t store it.
          </p>
          <Button type="submit" disabled={isSaving || !address.street.trim()}>
            {isSaving ? "Saving..." : "Save district"}
          </Button>
        </form>
      </div>

      {message && (
        <p role="status" className={`text-sm ${message.ok ? "text-success" : "text-destructive"}`}>
          {message.text}
        </p>
      )}

      {/* Always in the page, so a screen reader announces the message when it appears. */}
      <div role="alert">{repsError && <p className="text-sm text-destructive">{repsError}</p>}</div>

      {/* Representatives for the address or location just looked up */}
      {reps && (
        <div className="space-y-4">
          <h3 className="text-sm font-semibold text-foreground">Your Representatives</h3>
          <RepsList reps={reps} />
        </div>
      )}
    </div>
  );
}

/** A lookup's House member, senators and seat, with the election-district note (#375). */
function RepsList({ reps }: { reps: RepsResponse }) {
  return (
    <div className="space-y-4">
      {/* House Representatives */}
      {reps.reps.length > 0 && (
        <div className="space-y-2">
          {reps.reps.map((rep) => (
            <div key={rep.bioguide_id} className="flex items-center gap-3 rounded-lg border border-border p-3">
              <div className="min-w-0 flex-1">
                <p className="font-medium text-foreground">
                  {rep.first_name} {rep.last_name}
                </p>
                <p className="text-sm text-muted-foreground">
                  {seatsLabel(reps.districts) || "House Representative"}
                </p>
              </div>
            </div>
          ))}
        </div>
      )}

      <ElectionDistrictNote lookup={reps} />

      {/* Senators */}
      {reps.senators.length > 0 && (
        <div className="space-y-2">
          {reps.senators.map((sen) => (
            <div key={sen.bioguide_id} className="flex items-center gap-3 rounded-lg border border-border p-3">
              <div className="min-w-0 flex-1">
                <p className="font-medium text-foreground">
                  {sen.first_name} {sen.last_name}
                </p>
                <p className="text-sm text-muted-foreground">Senator</p>
              </div>
            </div>
          ))}
        </div>
      )}

      {reps.senators.length === 0 && noSenatorsNote(reps.districts) && (
        <p className="text-sm text-muted-foreground">{noSenatorsNote(reps.districts)}</p>
      )}

      {/* Districts info */}
      {reps.districts.length > 0 && (
        <p className="text-xs text-muted-foreground">
          House seat: {reps.districts.map((d) => districtName(d.state, d.district)).join(", ")}
        </p>
      )}

      {reps.reps.length === 0 && reps.senators.length === 0 && (
        <p className="text-sm text-muted-foreground">No representatives found for this address.</p>
      )}
    </div>
  );
}
