import { describe, expect, it } from "vitest";
import { addressLine, addressProblem, EMPTY_ADDRESS } from "@/components/reps/address-fields";

// The address fields /scorecard and Settings share (#668, #741): what they send, and when they're
// enough to look up.

describe("addressLine", () => {
  it.each([
    [{ street: "1 Main St", city: "Grand Junction", state: "CO", zip: "81501" }, "1 Main St, Grand Junction, CO 81501"],
    [{ street: "1 Main St", city: "", state: "", zip: "81501" }, "1 Main St, 81501"],
    [{ street: "1 Main St", city: "Grand Junction", state: "CO", zip: "" }, "1 Main St, Grand Junction, CO"],
    [
      { street: "  1 Main St ", city: " Grand Junction ", state: "CO", zip: " 81501 " },
      "1 Main St, Grand Junction, CO 81501",
    ],
  ])("joins %j into %s", (address, want) => {
    expect(addressLine(address)).toBe(want);
  });
});

describe("addressProblem", () => {
  it.each([
    ["a ZIP", { zip: "81501" }],
    ["a city and state", { city: "Grand Junction", state: "CO" }],
  ])("accepts a street with %s", (_, rest) => {
    expect(addressProblem({ ...EMPTY_ADDRESS, street: "1 Main St", ...rest })).toBeNull();
  });

  const NO_PLACE = "Add your ZIP code, or your city and state.";
  it.each([
    ["nothing", {}, "Enter your street address."],
    ["a blank street", { street: "  ", zip: "81501" }, "Enter your street address."],
    ["only a street", { street: "1 Main St" }, NO_PLACE],
    ["a city without a state", { street: "1 Main St", city: "Grand Junction" }, NO_PLACE],
    ["a state without a city", { street: "1 Main St", state: "CO" }, NO_PLACE],
  ])("names what's missing with %s", (_, rest, want) => {
    expect(addressProblem({ ...EMPTY_ADDRESS, ...rest })).toBe(want);
  });
});
