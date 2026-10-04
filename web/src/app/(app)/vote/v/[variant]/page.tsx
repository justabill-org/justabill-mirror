import type { Metadata } from "next";
import VotePage, { metadata as voteMetadata } from "../../page";

// The treatment arm of the A/A run on /vote (#695, docs/design/580-ab-experiments.md): src/proxy.ts
// rewrites half of /vote's visitors here, and the page is /vote itself, so the run checks the
// assignment, counting and readout with nothing else differing. Static like /vote: one prerendered
// param, and any other variant is a 404. The run's end PR deletes this route.
export const revalidate = 300; // REVALIDATE.list, as /vote; segment config must be a literal
export const dynamicParams = false;

export function generateStaticParams(): { variant: string }[] {
  return [{ variant: "treatment" }];
}

// Reached directly, it's a copy of /vote, so search engines are pointed there.
export const metadata: Metadata = { ...voteMetadata, alternates: { canonical: "/vote" } };

export default function VoteTreatmentPage() {
  return <VotePage />;
}
