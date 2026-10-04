import Link from "next/link";
import type { BillSponsors, SponsorEntry } from "@/lib/sponsors";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { PartyIndicator } from "@/components/member/party-indicator";
import { formatDate } from "@/lib/utils";

export function SponsorDisplay({ sponsor, cosponsors }: BillSponsors) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-lg">Sponsors</CardTitle>
      </CardHeader>
      <CardContent className="space-y-6">
        {/* Primary sponsor */}
        {sponsor && (
          <div>
            <h4 className="text-sm font-medium text-muted-foreground mb-3">Sponsor</h4>
            <SponsorCard sponsor={sponsor} isPrimary />
          </div>
        )}

        {/* Cosponsors */}
        {cosponsors.length > 0 && (
          <div>
            <h4 className="text-sm font-medium text-muted-foreground mb-3">
              Cosponsors ({cosponsors.length})
            </h4>
            <div className="space-y-2">
              {cosponsors.map((cosponsor) => (
                <SponsorCard key={cosponsor.bioguideId} sponsor={cosponsor} isCosponsor />
              ))}
            </div>
          </div>
        )}

        {!sponsor && cosponsors.length === 0 && (
          <p className="text-sm text-muted-foreground">No sponsor information available.</p>
        )}
      </CardContent>
    </Card>
  );
}

function SponsorCard({
  sponsor,
  isPrimary = false,
  isCosponsor = false,
}: {
  sponsor: SponsorEntry;
  isPrimary?: boolean;
  isCosponsor?: boolean;
}) {
  const details = [
    sponsor.party && getPartyLabel(sponsor.party),
    sponsor.state && (sponsor.district ? `${sponsor.state}-${sponsor.district}` : sponsor.state),
  ].filter(Boolean);

  return (
    <div className={`flex items-center gap-3 rounded-lg border border-border p-3 ${isPrimary ? "bg-muted/50" : ""}`}>
      <PartyIndicator party={sponsor.party ?? ""} size="lg" />
      <div className="min-w-0 flex-1">
        <Link
          href={`/members/${sponsor.bioguideId}`}
          className="block font-medium text-foreground truncate hover:underline"
        >
          {sponsor.name}
        </Link>
        {details.length > 0 && <p className="text-sm text-muted-foreground">{details.join(" - ")}</p>}
        {isCosponsor && sponsor.sponsoredDate && (
          <p className="text-xs text-muted-foreground mt-0.5">
            Added {formatDate(sponsor.sponsoredDate)}
            {sponsor.isOriginal && " (Original)"}
          </p>
        )}
      </div>
    </div>
  );
}

function getPartyLabel(party: string): string {
  const labels: Record<string, string> = {
    D: "Democrat",
    R: "Republican",
    I: "Independent",
    ID: "Independent",
  };
  return labels[party] || party;
}
