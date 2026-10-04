import { shareImageNotFound, shareImageResponse } from "@/components/share/image";
import { parseRepShare } from "@/lib/share";
import { loadRepCard } from "@/lib/share-data";

export async function GET(_request: Request, { params }: { params: Promise<{ member: string; score: string }> }) {
  const { member, score } = await params;
  const share = parseRepShare(member, score);
  const data = share ? await loadRepCard(share) : null;
  return data ? shareImageResponse(data.copy) : shareImageNotFound();
}
