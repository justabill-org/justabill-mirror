import type { MemberTerm } from "@/lib/types";
import { compareTermsNewestFirst, ordinal, termYearsLabel } from "@/lib/graph";
import { districtName } from "@/lib/districts";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { PartyIndicator } from "@/components/member/party-indicator";

interface MemberTermsProps {
  bioguideId: string;
  terms: MemberTerm[];
  /** The congresses Just a Bill has loaded (GET /congresses), or null when the list didn't load. */
  congresses: number[] | null;
}

/**
 * The member's terms in the congresses we load, newest first (#787), with a link to the
 * Biographical Directory for the rest of their service: we hold terms only for loaded congresses.
 */
export function MemberTerms({ bioguideId, terms, congresses }: MemberTermsProps) {
  const sorted = [...terms].sort(compareTermsNewestFirst);
  return (
    <section aria-label="Terms">
      <Card>
        <CardHeader>
          <CardTitle className="text-lg">Terms</CardTitle>
          <p className="text-sm text-muted-foreground">
            {coveredLine(congresses)} For their full service, see the{" "}
            <a
              href={`https://bioguide.congress.gov/search/bio/${encodeURIComponent(bioguideId)}`}
              target="_blank"
              rel="noopener noreferrer"
              className="underline underline-offset-2 hover:text-foreground"
            >
              Biographical Directory of the United States Congress
            </a>
            .
          </p>
        </CardHeader>
        <CardContent>
          {sorted.length === 0 ? (
            <p className="text-sm text-muted-foreground">No terms on record.</p>
          ) : (
            <ul className="-mt-3 divide-y divide-border">
              {sorted.map((term) => (
                <li key={`${term.congress}-${term.chamber}-${term.end_date ?? ""}`} className="py-3 text-sm">
                  <p className="font-medium text-foreground">
                    {ordinal(term.congress)} Congress
                    <span className="font-normal text-muted-foreground"> · {termYearsLabel(term)}</span>
                  </p>
                  <p className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-muted-foreground">
                    <span>
                      {term.chamber} · {seatOf(term)}
                    </span>
                    {term.party && (
                      <>
                        <span aria-hidden="true">·</span>
                        <PartyIndicator party={term.party} showLabel />
                      </>
                    )}
                  </p>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </section>
  );
}

/** "CA-12", "WY at-large" or, for a senator, the state. */
export function seatOf(term: Pick<MemberTerm, "chamber" | "state" | "district">): string {
  if (term.chamber === "Senate" || term.district === undefined || term.district === null) return term.state;
  return districtName(term.state, term.district);
}

/** "Terms in the 118th and 119th Congresses, the ones Just a Bill covers." */
function coveredLine(congresses: number[] | null): string {
  if (!congresses || congresses.length === 0) return "Terms in the congresses Just a Bill covers.";
  const names = [...congresses].sort((a, b) => a - b).map(ordinal);
  const list = new Intl.ListFormat("en-US", { type: "conjunction" }).format(names);
  const noun = names.length === 1 ? "Congress, the one" : "Congresses, the ones";
  return `Terms in the ${list} ${noun} Just a Bill covers.`;
}
