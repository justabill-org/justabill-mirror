import type { BillAction, BillLaw, BillTextDiff, BillTextVersion, GraphRelatedBill } from "@/lib/types";

/**
 * The Text section of a bill page (#665): which version is the final text, the earlier versions
 * in order with the changes between them, and the other chamber's companion bills. Plain helpers,
 * shared by the server page and the client panel.
 */

export type Chamber = "House" | "Senate";

/** GovInfo version codes whose text is what one chamber passed. */
const PASSED_CODES: Record<string, string> = {
  eh: "Passed the House",
  es: "Passed the Senate",
  cph: "Passed the House",
  cps: "Passed the Senate",
  eph: "Passed the House",
  eps: "Passed the Senate",
  eah: "House amendment, as passed",
  eas: "Senate amendment, as passed",
  ath: "Agreed to in the House",
  ats: "Agreed to in the Senate",
};

/** Plain-language steps for the other common codes; the official name is shown beside them. */
const STEP_CODES: Record<string, string> = {
  ih: "Introduced",
  is: "Introduced",
  rh: "Reported by committee",
  rs: "Reported by committee",
  rfh: "Referred to committee",
  rfs: "Referred to committee",
  rdh: "Received",
  rds: "Received",
  pch: "Placed on the calendar",
  pcs: "Placed on the calendar",
  enr: "Passed by both chambers",
  pl: "Became law",
  public_law: "Became law",
};

/** Official names for codes whose Congress.gov name is missing or terse. */
const OFFICIAL_NAMES: Record<string, string> = {
  enr: "Enrolled Bill",
  pl: "Public Law",
  public_law: "Public Law",
};

/** One version in the Versions list, with the changes from the version before it. */
export interface VersionStep {
  version: BillTextVersion;
  /** "Passed the Senate", or the official name when there's no plainer one. */
  step: string;
  /** Congress.gov's name for the version, e.g. "Engrossed in Senate". */
  officialName: string;
  /** The chamber the version belongs to; none for the enrolled bill and the public law. */
  chamber?: Chamber;
  /** The stored diff from the previous version to this one, when there is one. */
  changesFromPrevious?: BillTextDiff;
  previous?: BillTextVersion;
}

/** What leads the Text section: the final text, or the latest version and why it isn't final. */
export interface LeadText {
  version: BillTextVersion;
  isFinal: boolean;
  /** "Public Law 119-95" or "Private Law 117-3" when the bill became law (see {@link enactedLaws}). */
  law?: string;
  /** The date the bill became law, from the action that records it. */
  enactedDate?: string;
}

function code(version: BillTextVersion): string {
  return version.version_code.toLowerCase();
}

/** The chamber a version belongs to, read from its GovInfo code ("eh" → House, "es" → Senate). */
export function versionChamber(version: BillTextVersion): Chamber | undefined {
  const c = code(version);
  if (c === "enr" || c === "pl" || c === "public_law" || c === "pp" || c === "pap") return undefined;
  if (c.endsWith("h")) return "House";
  if (c.endsWith("s")) return "Senate";
  return undefined;
}

/** The chamber a bill type belongs to ("hr" → House, "sjres" → Senate). */
export function billChamber(billType: string): Chamber {
  return billType.toLowerCase().startsWith("h") ? "House" : "Senate";
}

export function otherChamber(chamber: Chamber): Chamber {
  return chamber === "House" ? "Senate" : "House";
}

/** The badge beside a version: its chamber, "Both chambers" for the enrolled bill, "Law" for the law. */
export function stageLabel(version: BillTextVersion): string | undefined {
  const c = code(version);
  if (c === "enr") return "Both chambers";
  if (c === "pl" || c === "public_law") return "Law";
  return versionChamber(version);
}

export function officialName(version: BillTextVersion): string {
  return OFFICIAL_NAMES[code(version)] ?? (version.version_type || version.version_code.toUpperCase());
}

/** Versions oldest first (sort_order 1 is the oldest; docs in pipeline/internal/sync/textversions.go). */
export function chronological(versions: readonly BillTextVersion[]): BillTextVersion[] {
  return [...versions].sort((a, b) => a.sort_order - b.sort_order);
}

/** The Public Law print, else the enrolled bill, else (for a simple resolution) the agreed text. */
export function finalVersion(versions: readonly BillTextVersion[], billType: string): BillTextVersion | undefined {
  const byCode = new Map(versions.map((v) => [code(v), v]));
  const simpleResolution = billType === "hres" || billType === "sres";
  return (
    byCode.get("pl") ??
    byCode.get("public_law") ??
    byCode.get("enr") ??
    (simpleResolution ? (byCode.get("ath") ?? byCode.get("ats")) : undefined)
  );
}

type LawType = "Public Law" | "Private Law";

/** A law's type as Congress.gov spells it, or undefined for anything else. */
function lawType(type: string): LawType | undefined {
  const t = type.trim().toLowerCase();
  if (t === "public law") return "Public Law";
  if (t === "private law") return "Private Law";
  return undefined;
}

const LAW_ACTION = /Became (Public|Private) Law No:\s*(\d+-\d+)/i;

/** The law and date from the action that records it ("Became Public Law No: 119-95."). */
export function lawFromActions(actions: readonly BillAction[] | null): { law: BillLaw; date: string } | undefined {
  for (const action of actions ?? []) {
    const match = LAW_ACTION.exec(action.action_text);
    const type = match && lawType(`${match[1]} Law`);
    if (match && type) return { law: { type, number: match[2] }, date: action.action_date };
  }
  return undefined;
}

/**
 * "Public Law 119-95" (or "Private Law 117-3"; several are joined with commas), from the bill's
 * own laws (Congress.gov's `laws`, #709). Only when those are missing, for rows the pipeline
 * hasn't refreshed yet, is it read from the action text. Undefined when neither has one.
 */
export function enactedLaws(laws: readonly BillLaw[] | undefined, actions: readonly BillAction[] | null): string | undefined {
  const names = (laws ?? []).flatMap((law) => {
    const type = lawType(law.type);
    return type && law.number ? [`${type} ${law.number.trim()}`] : [];
  });
  if (names.length > 0) return names.join(", ");
  const fromAction = lawFromActions(actions)?.law;
  return fromAction ? `${fromAction.type} ${fromAction.number}` : undefined;
}

/** The version the Text section leads with. Undefined when the bill has no text yet. */
export function leadText(
  versions: readonly BillTextVersion[],
  billType: string,
  actions: readonly BillAction[] | null,
  laws?: readonly BillLaw[],
): LeadText | undefined {
  const final = finalVersion(versions, billType);
  if (final) {
    return { version: final, isFinal: true, law: enactedLaws(laws, actions), enactedDate: lawFromActions(actions)?.date };
  }
  const ordered = chronological(versions);
  const latest = ordered[ordered.length - 1];
  return latest ? { version: latest, isFinal: false } : undefined;
}

/** Every version oldest first, each with the stored diff from the version before it. */
export function versionSteps(versions: readonly BillTextVersion[], diffs: readonly BillTextDiff[] | null): VersionStep[] {
  const ordered = chronological(versions);
  return ordered.map((version, i) => {
    const previous = i > 0 ? ordered[i - 1] : undefined;
    const c = code(version);
    const changesFromPrevious = previous
      ? (diffs ?? []).find((d) => d.from_version_id === previous.id && d.to_version_id === version.id)
      : undefined;
    return {
      version,
      step: PASSED_CODES[c] ?? STEP_CODES[c] ?? officialName(version),
      officialName: officialName(version),
      chamber: versionChamber(version),
      changesFromPrevious,
      previous,
    };
  });
}

/** Congress.gov relation types that mark a bill as the other chamber's version of this one. */
const COMPANION_RELATIONS = ["Identical bill", "Related bill"];

/** The other chamber's bills that Congress.gov lists as identical or related, identical first. */
export function companionBills(related: readonly GraphRelatedBill[], billType: string): GraphRelatedBill[] {
  const other = otherChamber(billChamber(billType));
  return related
    .filter((r) => billChamber(r.bill_type) === other)
    .filter((r) => r.relation_types.some((t) => COMPANION_RELATIONS.includes(t)))
    .sort((a, b) => Number(b.relation_types.includes("Identical bill")) - Number(a.relation_types.includes("Identical bill")));
}

