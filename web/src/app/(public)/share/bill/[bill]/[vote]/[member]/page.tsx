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

interface BillMemberSharePageProps {
  params: Promise<{ bill: string; vote: string; member: string }>;
}

const load = cache(async (bill: string, vote: string, member: string) => {
  const share = parseBillShare(bill, vote, member);
  const data = share ? await loadBillCard(share) : null;
  return share && data ? { url: billShareUrl(share), data } : null;
});

export async function generateMetadata({ params }: BillMemberSharePageProps): Promise<Metadata> {
  const { bill, vote, member } = await params;
  const card = await load(bill, vote, member);
  if (!card) notFound();
  return shareMetadata(card.url, card.data.copy);
}

export default async function BillMemberSharePage({ params }: BillMemberSharePageProps) {
  const { bill, vote, member } = await params;
  const card = await load(bill, vote, member);
  if (!card) notFound();
  return <SharePage data={card.data} />;
}
