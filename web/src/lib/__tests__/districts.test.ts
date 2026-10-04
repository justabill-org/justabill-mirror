import { describe, it, expect } from "vitest";
import { districtName, hasSenators, noSenatorsNote, seatLabel, seatsLabel } from "../districts";

const AT_LARGE_STATES = ["AK", "DE", "ND", "SD", "VT", "WY"];
const DELEGATE_AREAS = ["DC", "GU", "VI", "AS", "MP"];

describe("seatLabel", () => {
  it.each(AT_LARGE_STATES)("labels district 0 in %s At-large", (state) => {
    expect(seatLabel(state, 0)).toBe("At-large");
  });

  it.each(DELEGATE_AREAS)("labels district 0 in %s a non-voting delegate", (state) => {
    expect(seatLabel(state, 0)).toBe("Delegate (non-voting)");
  });

  it("labels Puerto Rico's seat Resident Commissioner", () => {
    expect(seatLabel("PR", 0)).toBe("Resident Commissioner (non-voting)");
  });

  it("numbers other districts and never says Unknown", () => {
    expect(seatLabel("TX", 10)).toBe("District 10");
    expect(seatLabel("CA", 1)).toBe("District 1");
    for (const state of [...AT_LARGE_STATES, ...DELEGATE_AREAS, "PR", "MT"]) {
      expect(seatLabel(state, 0)).not.toMatch(/Unknown|District 0/);
    }
  });
});

describe("districtName", () => {
  it("writes numbered districts as ST-N", () => {
    expect(districtName("TX", 10)).toBe("TX-10");
  });

  it("never writes a single seat as ST-0", () => {
    expect(districtName("WY", 0)).toBe("WY at-large");
    expect(districtName("GU", 0)).toBe("GU delegate (non-voting)");
    expect(districtName("PR", 0)).toBe("PR resident commissioner (non-voting)");
  });
});

describe("seatsLabel", () => {
  it("gives the seat label for one district", () => {
    expect(seatsLabel([{ state: "AK", district: 0, at_large: true, source: "geocoder" }])).toBe("At-large");
    expect(seatsLabel([{ state: "TX", district: 10 }])).toBe("District 10");
  });

  it("names every district when an address straddles several", () => {
    expect(seatsLabel([{ state: "TX", district: 10 }, { state: "TX", district: 21 }])).toBe("TX-10, TX-21");
  });

  it("is empty when nothing matched", () => {
    expect(seatsLabel([])).toBe("");
  });
});

describe("hasSenators", () => {
  it("is false only for DC and the territories", () => {
    for (const state of [...DELEGATE_AREAS, "PR"]) expect(hasSenators(state)).toBe(false);
    for (const state of [...AT_LARGE_STATES, "TX", "CA"]) expect(hasSenators(state)).toBe(true);
  });
});

describe("noSenatorsNote", () => {
  it.each([
    ["DC", "No U.S. senators represent the District of Columbia."],
    ["PR", "No U.S. senators represent Puerto Rico."],
    ["GU", "No U.S. senators represent Guam."],
    ["VI", "No U.S. senators represent the U.S. Virgin Islands."],
    ["AS", "No U.S. senators represent American Samoa."],
    ["MP", "No U.S. senators represent the Northern Mariana Islands."],
  ])("explains why %s has no senators", (state, note) => {
    expect(noSenatorsNote([{ state, district: 0, at_large: true, source: "state" }])).toBe(note);
  });

  it("is null for states, which have senators", () => {
    expect(noSenatorsNote([{ state: "WY", district: 0 }])).toBeNull();
    expect(noSenatorsNote([{ state: "TX", district: 10 }])).toBeNull();
  });

  it("is null when nothing matched", () => {
    expect(noSenatorsNote([])).toBeNull();
  });
});
