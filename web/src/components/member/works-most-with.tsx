import Link from "next/link";
import type { Collaborator } from "@/lib/types";
import { ordinal } from "@/lib/graph";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { PartyIndicator } from "@/components/member/party-indicator";

interface WorksMostWithProps {
  memberName: string;
  congress: number;
  collaborators: Collaborator[];
}

const partyBars: Record<string, string> = {
  D: "bg-party-d",
  R: "bg-party-r",
  I: "bg-party-i",
  ID: "bg-party-i",
};

// Counts only: the panel lists who shares the most sponsored and cosponsored bills, with no
// scoring or labels on top (docs/design/31-bill-ontology.md, "Nonpartisan presentation").
export function WorksMostWith({ memberName, congress, collaborators }: WorksMostWithProps) {
  const most = Math.max(1, ...collaborators.map((c) => c.shared_bills));

  return (
    <section aria-label="Works most with">
      <Card>
        <CardHeader>
          <CardTitle className="text-lg">Works Most With</CardTitle>
          <p className="text-sm text-muted-foreground">
            Members who sponsored or cosponsored the most bills with {memberName} in the {ordinal(congress)}{" "}
            Congress, from public sponsorship records.
          </p>
        </CardHeader>
        <CardContent>
          {collaborators.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              No shared bills recorded for the {ordinal(congress)} Congress.
            </p>
          ) : (
            <ul className="space-y-3">
              {collaborators.map((c) => (
                <li key={c.bioguide_id}>
                  <div className="flex items-center justify-between gap-3 text-sm">
                    <Link
                      href={`/members/${c.bioguide_id}`}
                      className="flex min-w-0 items-center gap-2 font-medium text-foreground hover:underline"
                    >
                      <PartyIndicator party={c.party ?? ""} />
                      <span className="truncate">
                        {c.first_name} {c.last_name}
                      </span>
                    </Link>
                    <span className="shrink-0 text-muted-foreground">
                      {[c.state, c.chamber].filter(Boolean).join(" · ")}
                      {" · "}
                      {c.shared_bills} {c.shared_bills === 1 ? "bill" : "bills"}
                    </span>
                  </div>
                  <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-muted" aria-hidden="true">
                    <div
                      className={`h-full rounded-full ${partyBars[c.party ?? ""] ?? "bg-muted-foreground"}`}
                      style={{ width: `${(c.shared_bills / most) * 100}%` }}
                    />
                  </div>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </section>
  );
}
