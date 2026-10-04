import { describe, expect, it } from "vitest";
import type { BillAction, BillTextDiff, BillTextVersion, GraphRelatedBill } from "@/lib/types";
import {
  billChamber,
  chronological,
  companionBills,
  enactedLaws,
  finalVersion,
  lawFromActions,
  leadText,
  officialName,
  stageLabel,
  versionChamber,
  versionSteps,
} from "@/lib/text-versions";

function version(code: string, sortOrder: number, versionType = code.toUpperCase()): BillTextVersion {
  return {
    id: `v-${code}`,
    bill_id: "s-119-4530",
    version_type: versionType,
    version_code: code,
    date: `2026-0${sortOrder}-01T00:00:00Z`,
    formats: [],
    sort_order: sortOrder,
  };
}

function action(text: string, date = "2026-08-04T00:00:00Z"): BillAction {
  return { id: text, bill_id: "s-119-4530", action_date: date, action_text: text, sort_order: 1 };
}

function diff(from: BillTextVersion, to: BillTextVersion): BillTextDiff {
  return { id: `d-${from.version_code}-${to.version_code}`, bill_id: from.bill_id, from_version_id: from.id, to_version_id: to.id };
}

function related(billType: string, number: number, relationTypes: string[]): GraphRelatedBill {
  return {
    bill_id: `${billType}-119-${number}`,
    congress: 119,
    bill_type: billType,
    number,
    title: "Companion",
    relation_types: relationTypes,
    shared_subjects: 0,
  };
}

// S. 4530's versions as GovInfo lists them, out of order as the API may send them.
const es = version("es", 1, "Engrossed in Senate");
const cps = version("cps", 2, "Considered and Passed Senate");
const enr = version("enr", 3, "");
const pl = version("pl", 4, "");
const lawActions = [action("Presented to President."), action("Became Public Law No: 119-95.")];

describe("finalVersion", () => {
  it("is the Public Law print when there is one", () => {
    expect(finalVersion([enr, es, pl, cps], "s")).toBe(pl);
  });

  it("is the enrolled bill until GovInfo prints the Public Law", () => {
    expect(finalVersion([es, enr, cps], "s")).toBe(enr);
  });

  it("is none for a bill that passed only one chamber", () => {
    expect(finalVersion([es, cps], "s")).toBeUndefined();
  });

  it("is the agreed-to text for a simple resolution, which never goes to the other chamber", () => {
    const ats = version("ats", 2, "Agreed to Senate");
    expect(finalVersion([version("is", 1), ats], "sres")).toBe(ats);
    expect(finalVersion([version("is", 1), ats], "sjres")).toBeUndefined();
  });
});

describe("lawFromActions", () => {
  it("reads the law and date from the action that records it", () => {
    expect(lawFromActions(lawActions)).toEqual({
      law: { type: "Public Law", number: "119-95" },
      date: "2026-08-04T00:00:00Z",
    });
  });

  it("reads a private law as a private law", () => {
    expect(lawFromActions([action("Became Private Law No: 117-3.", "2023-01-05T00:00:00Z")])).toEqual({
      law: { type: "Private Law", number: "117-3" },
      date: "2023-01-05T00:00:00Z",
    });
  });

  it("is none without that action", () => {
    expect(lawFromActions(null)).toBeUndefined();
    expect(lawFromActions([action("Presented to President.")])).toBeUndefined();
  });
});

describe("enactedLaws", () => {
  const stored = [{ type: "Public Law", number: "119-95" }];

  it("names the law from the bill's own laws, whatever the action text says", () => {
    expect(enactedLaws(stored, [action("Became Public Law No: 119-9.")])).toBe("Public Law 119-95");
    expect(enactedLaws(stored, null)).toBe("Public Law 119-95");
  });

  it("labels a private law as a Private Law, never a Public Law", () => {
    expect(enactedLaws([{ type: "Private Law", number: "117-3" }], null)).toBe("Private Law 117-3");
    expect(enactedLaws(undefined, [action("Became Private Law No: 117-3.")])).toBe("Private Law 117-3");
  });

  it("names every law a bill became", () => {
    expect(enactedLaws([...stored, { type: "Public Law", number: "119-96" }], null)).toBe(
      "Public Law 119-95, Public Law 119-96",
    );
  });

  it("falls back to the action text only when the bill has no laws yet", () => {
    expect(enactedLaws(undefined, lawActions)).toBe("Public Law 119-95");
    expect(enactedLaws([], lawActions)).toBe("Public Law 119-95");
  });

  it("skips a law of a type it doesn't know", () => {
    expect(enactedLaws([{ type: "Treaty", number: "1" }], null)).toBeUndefined();
  });

  it("is none for a bill that isn't law", () => {
    expect(enactedLaws(undefined, [action("Presented to President.")])).toBeUndefined();
  });
});

describe("leadText", () => {
  it("leads a law with its final text, Public Law number and the date it became law", () => {
    expect(leadText([es, cps, enr, pl], "s", lawActions)).toEqual({
      version: pl,
      isFinal: true,
      law: "Public Law 119-95",
      enactedDate: "2026-08-04T00:00:00Z",
    });
  });

  it("takes the number from the bill's laws when it has them", () => {
    expect(leadText([enr], "s", lawActions, [{ type: "Public Law", number: "119-96" }])).toMatchObject({
      law: "Public Law 119-96",
      enactedDate: "2026-08-04T00:00:00Z",
    });
  });

  it("leads a bill that passed one chamber with its latest version, not final", () => {
    expect(leadText([cps, es], "s", [])).toEqual({ version: cps, isFinal: false });
  });

  it("is none for a bill without text", () => {
    expect(leadText([], "hr", null)).toBeUndefined();
  });
});

describe("versionSteps", () => {
  it("lists every version oldest first with the diff from the version before it", () => {
    const d1 = diff(es, cps);
    const d2 = diff(cps, enr);
    const steps = versionSteps([pl, enr, es, cps], [d2, d1]);

    expect(steps.map((s) => s.step)).toEqual([
      "Passed the Senate",
      "Passed the Senate",
      "Passed by both chambers",
      "Became law",
    ]);
    expect(steps.map((s) => s.officialName)).toEqual([
      "Engrossed in Senate",
      "Considered and Passed Senate",
      "Enrolled Bill",
      "Public Law",
    ]);
    expect(steps.map((s) => s.chamber)).toEqual(["Senate", "Senate", undefined, undefined]);
    expect(steps.map((s) => s.previous?.id)).toEqual([undefined, es.id, cps.id, enr.id]);
    expect(steps.map((s) => s.changesFromPrevious?.id)).toEqual([undefined, d1.id, d2.id, undefined]);
  });

  it("keeps the official name as the step when there's no plainer one", () => {
    const [step] = versionSteps([version("xyz", 1, "Some New Stage")], null);
    expect(step.step).toBe("Some New Stage");
  });
});

describe("chambers and labels", () => {
  it("reads the chamber from the GovInfo code", () => {
    expect(versionChamber(version("ih", 1))).toBe("House");
    expect(versionChamber(version("rs", 1))).toBe("Senate");
    expect(versionChamber(version("enr", 1))).toBeUndefined();
    expect(versionChamber(version("pp", 1))).toBeUndefined();
  });

  it("labels each version's stage, and none when the code names no chamber", () => {
    expect(stageLabel(version("EH", 1))).toBe("House");
    expect(stageLabel(enr)).toBe("Both chambers");
    expect(stageLabel(pl)).toBe("Law");
    expect(stageLabel(version("pp", 1))).toBeUndefined();
  });

  it("reads the chamber from the bill type", () => {
    expect(billChamber("hjres")).toBe("House");
    expect(billChamber("sconres")).toBe("Senate");
  });

  it("falls back to the code when Congress.gov gives no name", () => {
    expect(officialName(version("rfh", 1, ""))).toBe("RFH");
  });

  it("sorts by sort_order without changing the input", () => {
    const input = [cps, es];
    expect(chronological(input)).toEqual([es, cps]);
    expect(input).toEqual([cps, es]);
  });
});

describe("companionBills", () => {
  it("keeps the other chamber's identical and related bills, identical first", () => {
    const bills = [
      related("hr", 8364, ["Related bill"]),
      related("s", 12, ["Identical bill"]),
      related("hr", 9000, ["Procedurally-related"]),
      related("hr", 7000, ["Identical bill", "Related bill"]),
    ];
    expect(companionBills(bills, "s").map((b) => b.bill_id)).toEqual(["hr-119-7000", "hr-119-8364"]);
    expect(companionBills(bills, "hr").map((b) => b.bill_id)).toEqual(["s-119-12"]);
  });
});
