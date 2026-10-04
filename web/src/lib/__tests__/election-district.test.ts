import { describe, expect, it } from "vitest";
import { electionDistrictLine, type ElectionLookup } from "../election-district";

// #375: the "you vote in" line (docs/design/237-election-district.md, "When the line shows").

const austin: ElectionLookup = {
  districts: [{ state: "TX", district: 37 }],
  election: {
    congress: 120,
    election_date: "2026-11-03",
    districts: [{ state: "TX", district: 10 }],
    changed: true,
  },
};

const SOURCE = "Map: U.S. Census Bureau, 120th Congress districts as submitted by the state.";

/** Noon local time, so the visitor's date is the one named whatever the test machine's zone. */
function day(iso: string, hour = 12): Date {
  const [y, m, d] = iso.split("-").map(Number);
  return new Date(y, m - 1, d, hour);
}

function text(line: ReturnType<typeof electionDistrictLine>): string | null {
  return line && `${line.lead}${line.districts}${line.rest}`;
}

describe("electionDistrictLine", () => {
  it.each(["2026-10-04", "2026-11-03"])("names the new district and today's on %s", (today) => {
    const line = electionDistrictLine(austin, day(today));
    expect(line).toEqual({
      lead: "On November 3, 2026, this address votes in the election for ",
      districts: "TX-10",
      rest: ". District lines changed for 2026, so this isn't the district your current representative holds (TX-37).",
      source: SOURCE,
    });
  });

  it("still says the election is ahead late on Election Day", () => {
    expect(electionDistrictLine(austin, day("2026-11-03", 23))?.lead).toMatch(/^On November 3, 2026/);
  });

  it.each(["2026-11-04", "2027-01-02"])("says when the new district starts on %s", (today) => {
    const line = electionDistrictLine(austin, day(today));
    expect(line).toEqual({ lead: "From January 3, 2027, this address is in ", districts: "TX-10", rest: ".", source: SOURCE });
  });

  it("says nothing once the new congress is seated", () => {
    expect(electionDistrictLine(austin, day("2027-01-03"))).toBeNull();
  });

  it("says nothing when the districts didn't change", () => {
    expect(electionDistrictLine({ ...austin, election: { ...austin.election!, changed: false } }, day("2026-10-04"))).toBeNull();
  });

  it.each([
    ["missing", undefined],
    ["null", null],
  ])("says nothing when the election block is %s", (_, election) => {
    expect(electionDistrictLine({ districts: austin.districts, election }, day("2026-10-04"))).toBeNull();
  });

  it("says nothing for a malformed date or no districts", () => {
    const e = austin.election!;
    expect(electionDistrictLine({ ...austin, election: { ...e, election_date: "Nov 3" } }, day("2026-10-04"))).toBeNull();
    expect(electionDistrictLine({ ...austin, election: { ...e, districts: [] } }, day("2026-10-04"))).toBeNull();
  });

  it("names every district the address matches", () => {
    const lookup: ElectionLookup = {
      districts: [
        { state: "TX", district: 37 },
        { state: "TX", district: 35 },
      ],
      election: {
        ...austin.election!,
        districts: [
          { state: "TX", district: 10 },
          { state: "TX", district: 21 },
          { state: "TX", district: 10 },
        ],
      },
    };
    const line = electionDistrictLine(lookup, day("2026-10-04"));
    expect(line?.districts).toBe("TX-10 or TX-21");
    expect(line?.rest).toContain("(TX-37 or TX-35)");
  });

  it("uses the at-large and delegate labels", () => {
    const lookup: ElectionLookup = {
      districts: [{ state: "DC", district: 0 }],
      election: { ...austin.election!, districts: [{ state: "WY", district: 0 }] },
    };
    expect(text(electionDistrictLine(lookup, day("2026-10-04")))).toBe(
      "On November 3, 2026, this address votes in the election for WY at-large. District lines changed for 2026, " +
        "so this isn't the district your current representative holds (DC delegate (non-voting))."
    );
  });

  it("drops the comparison when today's district is unknown", () => {
    const line = electionDistrictLine({ ...austin, districts: [] }, day("2026-10-04"));
    expect(line?.rest).toBe(". District lines changed for 2026.");
  });

  it("takes the years and dates from the election block, not the calendar", () => {
    const lookup: ElectionLookup = {
      districts: [{ state: "MO", district: 3 }],
      election: { congress: 121, election_date: "2028-11-07", districts: [{ state: "MO", district: 5 }], changed: true },
    };
    expect(text(electionDistrictLine(lookup, day("2028-10-01")))).toBe(
      "On November 7, 2028, this address votes in the election for MO-5. District lines changed for 2028, " +
        "so this isn't the district your current representative holds (MO-3)."
    );
    expect(electionDistrictLine(lookup, day("2028-10-01"))?.source).toContain("121st Congress districts");
    expect(text(electionDistrictLine(lookup, day("2028-12-01")))).toBe("From January 3, 2029, this address is in MO-5.");
  });
});
