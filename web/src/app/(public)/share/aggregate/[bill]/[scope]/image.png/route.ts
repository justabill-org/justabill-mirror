import { AGGREGATE_IMAGE_CACHE_CONTROL, shareImageNotFound, shareImageResponse } from "@/components/share/image";
import { parseAggregateShare } from "@/lib/share";
import { loadAggregateCard } from "@/lib/share-data";

export async function GET(_request: Request, { params }: { params: Promise<{ bill: string; scope: string }> }) {
  const { bill, scope } = await params;
  const share = parseAggregateShare(bill, scope);
  const data = share ? await loadAggregateCard(share) : null;
  return data ? shareImageResponse(data.copy, AGGREGATE_IMAGE_CACHE_CONTROL) : shareImageNotFound();
}
