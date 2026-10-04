import { shareImageNotFound, shareImageResponse } from "@/components/share/image";
import { parseBillShare } from "@/lib/share";
import { loadBillCard } from "@/lib/share-data";

export async function GET(_request: Request, { params }: { params: Promise<{ bill: string; vote: string }> }) {
  const { bill, vote } = await params;
  const share = parseBillShare(bill, vote);
  const data = share ? await loadBillCard(share) : null;
  return data ? shareImageResponse(data.copy) : shareImageNotFound();
}
