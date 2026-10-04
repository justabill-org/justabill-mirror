import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { cache } from "react";
import { SharePage } from "@/components/share/share-page";
import { parseRepShare, repShareUrl } from "@/lib/share";
import { loadRepCard } from "@/lib/share-data";
import { shareMetadata } from "@/lib/share-metadata";

// On-demand ISR: nothing is prebuilt, each card is rendered on first request and kept a day.
export const revalidate = 86400;

export function generateStaticParams() {
  return [];
}

interface RepSharePageProps {
  params: Promise<{ member: string; score: string }>;
}

const load = cache(async (member: string, score: string) => {
  const share = parseRepShare(member, score);
  const data = share ? await loadRepCard(share) : null;
  return share && data ? { url: repShareUrl(share), data } : null;
});

export async function generateMetadata({ params }: RepSharePageProps): Promise<Metadata> {
  const { member, score } = await params;
  const card = await load(member, score);
  if (!card) notFound();
  return shareMetadata(card.url, card.data.copy);
}

export default async function RepSharePage({ params }: RepSharePageProps) {
  const { member, score } = await params;
  const card = await load(member, score);
  if (!card) notFound();
  return <SharePage data={card.data} />;
}
