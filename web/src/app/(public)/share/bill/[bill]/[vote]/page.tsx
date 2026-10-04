import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { cache } from "react";
import { SharePage } from "@/components/share/share-page";
import { billShareUrl, parseBillShare } from "@/lib/share";
import { loadBillCard } from "@/lib/share-data";
import { shareMetadata } from "@/lib/share-metadata";

// On-demand ISR: nothing is prebuilt, each card is rendered on first request and kept a day.
export const revalidate = 86400;

export function generateStaticParams() {
  return [];
}

interface BillSharePageProps {
  params: Promise<{ bill: string; vote: string }>;
}

const load = cache(async (bill: string, vote: string) => {
  const share = parseBillShare(bill, vote);
  const data = share ? await loadBillCard(share) : null;
  return share && data ? { url: billShareUrl(share), data } : null;
});

export async function generateMetadata({ params }: BillSharePageProps): Promise<Metadata> {
  const { bill, vote } = await params;
  const card = await load(bill, vote);
  if (!card) notFound();
  return shareMetadata(card.url, card.data.copy);
}

export default async function BillSharePage({ params }: BillSharePageProps) {
  const { bill, vote } = await params;
  const card = await load(bill, vote);
  if (!card) notFound();
  return <SharePage data={card.data} />;
}
