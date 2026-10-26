"use client";

import type { LocalRep } from "@/lib/local/reps";
import { memberTitle } from "@/lib/share";
import type { BillSponsorship } from "@/lib/types";
import { formatDate } from "@/lib/utils";
import { useLocalReps } from "@/lib/votes/hooks";

interface UnrecordedRepsProps {
  /** The chamber that passed the measure without a roll call: "House" or "Senate". */
  chamber: string;
  /** The bill's sponsor and cosponsors; null when the read failed. */
  sponsorships: BillSponsorship[] | null;
}

/**
 * What each of the visitor's saved representatives in the voting chamber did about a bill that
 * passed by voice vote or unanimous consent. Sponsorship, from Congress.gov, is the only fact on
 * record for a member there, so anyone else reads "no recorded position" and nothing is inferred
 * (#1002). The reps come from this browser and nothing is sent. It renders nothing on the server,
 * during hydration, or without reps in that chamber.
 */
export function UnrecordedReps({ chamber, sponsorships }: UnrecordedRepsProps) {
  const reps = useLocalReps();
  const members = reps?.members.filter((m) => m.chamber === chamber) ?? [];
  if (members.length === 0) return null;

  const byMember = new Map((sponsorships ?? []).map((s) => [s.bioguide_id, s]));
  const anySponsor = members.some((m) => byMember.has(m.id));

  return (
    <div className="mt-4 border-t border-border pt-3">
      <h4 className="text-sm font-medium text-foreground">Your representatives</h4>
      {!anySponsor && (
        <p className="mt-1 text-sm text-muted-foreground">
          A voice vote or unanimous consent records no individual positions.
        </p>
      )}
      <ul className="mt-2 space-y-1 text-sm text-muted-foreground">
        {members.map((m) => (
          <li key={m.id}>{repLine(m, byMember.get(m.id))}</li>
        ))}
      </ul>
    </div>
  );
}

/** "Rep. Jane Doe cosponsored this bill (Mar 18, 2025)", or "Sen. Ann Poe: no recorded position". */
function repLine(rep: LocalRep, sponsorship: BillSponsorship | undefined): string {
  const name = `${memberTitle(rep)} ${rep.name}`;
  if (!sponsorship) return `${name}: no recorded position`;
  if (sponsorship.role === "sponsor") return `${name} sponsored this bill`;
  const date = sponsorship.sponsored_date ? ` (${formatDate(sponsorship.sponsored_date)})` : "";
  return `${name} cosponsored this bill${date}`;
}
