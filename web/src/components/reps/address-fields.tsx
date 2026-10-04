"use client";

import { useId, type Ref } from "react";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { US_STATES } from "@/lib/us-states";

/** A home address as its four fields hold it; the state is its two-letter code, or "". */
export interface Address {
  street: string;
  city: string;
  state: string;
  zip: string;
}

export const EMPTY_ADDRESS: Address = { street: "", city: "", state: "", zip: "" };

/** The one line the Census geocoder reads: "street, city, ST zip", leaving out what's blank. */
export function addressLine({ street, city, state, zip }: Address): string {
  return [street.trim(), city.trim(), `${state} ${zip.trim()}`.trim()].filter(Boolean).join(", ");
}

/** Why the address can't be looked up yet, or null when it can: a street plus a ZIP or a city and state. */
export function addressProblem({ street, city, state, zip }: Address): string | null {
  if (!street.trim()) return "Enter your street address.";
  if (!zip.trim() && !(city.trim() && state)) return "Add your ZIP code, or your city and state.";
  return null;
}

/**
 * Street, city, state and ZIP (#668, #741), each with the autocomplete token browser autofill
 * fills: a single `street-address` box gets only the street line from Chrome and Safari. No
 * type-ahead. Shared by /scorecard's FindRepsForm and the Settings form.
 */
export function AddressFields({
  value,
  onChange,
  streetRef,
}: {
  value: Address;
  onChange: (next: Address) => void;
  /** The street field, where focus goes when "Use my location" fails. */
  streetRef?: Ref<HTMLInputElement>;
}) {
  const ids = useId();
  const set = (key: keyof Address) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) =>
    onChange({ ...value, [key]: e.target.value });

  return (
    <>
      <div className="space-y-1">
        <label htmlFor={`${ids}-street`} className="text-sm font-medium text-foreground">
          Street address
        </label>
        <Input
          ref={streetRef}
          id={`${ids}-street`}
          name="address-line1"
          value={value.street}
          onChange={set("street")}
          autoComplete="address-line1"
          placeholder="e.g. 123 Main St"
          required
        />
      </div>

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-[1fr_11rem_7rem]">
        <div className="col-span-2 space-y-1 sm:col-span-1">
          <label htmlFor={`${ids}-city`} className="text-sm font-medium text-foreground">
            City
          </label>
          <Input
            id={`${ids}-city`}
            name="address-level2"
            value={value.city}
            onChange={set("city")}
            autoComplete="address-level2"
          />
        </div>
        <div className="space-y-1">
          <label htmlFor={`${ids}-state`} className="text-sm font-medium text-foreground">
            State
          </label>
          <Select
            id={`${ids}-state`}
            name="address-level1"
            value={value.state}
            onChange={set("state")}
            autoComplete="address-level1"
          >
            <option value="">Choose…</option>
            {US_STATES.map((s) => (
              <option key={s.code} value={s.code}>
                {s.name}
              </option>
            ))}
          </Select>
        </div>
        <div className="space-y-1">
          <label htmlFor={`${ids}-zip`} className="text-sm font-medium text-foreground">
            ZIP code
          </label>
          <Input
            id={`${ids}-zip`}
            name="postal-code"
            value={value.zip}
            onChange={set("zip")}
            autoComplete="postal-code"
            inputMode="numeric"
            pattern="\d{5}(-\d{4})?"
            title="Five digits, or ZIP+4"
          />
        </div>
      </div>
    </>
  );
}
