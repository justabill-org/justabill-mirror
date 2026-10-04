import { crsAbstract } from "@/lib/crs";
import { billJsonLd, serializeJsonLd, siteOrigin } from "@/lib/seo";
import type { SponsorEntry } from "@/lib/sponsors";
import type { Bill, CrsSummary } from "@/lib/types";

interface BillJsonLdProps {
  bill: Bill;
  sponsor?: Pick<SponsorEntry, "bioguideId" | "name">;
  /** Its first paragraph becomes the abstract. */
  crsSummary?: CrsSummary | null;
}

/** The bill as schema.org Legislation, for search engines (#86). */
export function BillJsonLd({ bill, sponsor, crsSummary }: BillJsonLdProps) {
  const abstract = crsSummary ? crsAbstract(crsSummary.text) : undefined;
  return (
    <script
      type="application/ld+json"
      // Serialized data, not markup: serializeJsonLd escapes "<" so it can't close the tag.
      dangerouslySetInnerHTML={{ __html: serializeJsonLd(billJsonLd(bill, siteOrigin(), sponsor, abstract)) }}
    />
  );
}
