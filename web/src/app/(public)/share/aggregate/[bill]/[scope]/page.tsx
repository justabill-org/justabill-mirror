import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { cache } from "react";
import { SharePage } from "@/components/share/share-page";
import { aggregateShareUrl, parseAggregateShare } from "@/lib/share";
import { loadAggregateCard } from "@/lib/share-data";
import { shareMetadata } from "@/lib/share-metadata";

// On-demand ISR, kept REVALIDATE.bill (10 minutes) like the aggregates fetch, rather than the day
// other cards keep: the cells change every hour, and a held cell must stop being shown.
export const revalidate = 600;

export function generateStaticParams() {
  return [];
}

interface AggregateSharePageProps {
  params: Promise<{ bill: string; scope: string }>;
}

const load = cache(async (bill: string, scope: string) => {
  const share = parseAggregateShare(bill, scope);
  const data = share ? await loadAggregateCard(share) : null;
  return share && data ? { url: aggregateShareUrl(share), data } : null;
});

export async function generateMetadata({ params }: AggregateSharePageProps): Promise<Metadata> {
  const { bill, scope } = await params;
  const card = await load(bill, scope);
  if (!card) notFound();
  return shareMetadata(card.url, card.data.copy);
}

export default async function AggregateSharePage({ params }: AggregateSharePageProps) {
  const { bill, scope } = await params;
  const card = await load(bill, scope);
  if (!card) notFound();
  return <SharePage data={card.data} />;
}
