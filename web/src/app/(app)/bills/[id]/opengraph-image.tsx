import { shareImageNotFound, shareImageResponse } from "@/components/share/image";
import { ApiError, getBill } from "@/lib/api";
import { BILL_IMAGE_CACHE_CONTROL, billPreviewCopy } from "@/lib/seo";
import { SHARE_IMAGE_HEIGHT, SHARE_IMAGE_WIDTH, parseBillId } from "@/lib/share";

// The bill page's link-preview image (#86): number, congress, title and status, on the share
// card's layout (#88). Next.js adds it to the page's og:image; twitter-image.tsx reuses it.

export const alt = "The bill's number, title and status on Just a Bill";
export const size = { width: SHARE_IMAGE_WIDTH, height: SHARE_IMAGE_HEIGHT };
export const contentType = "image/png";

export default async function Image({ params }: { params: Promise<{ id: string }> }): Promise<Response> {
  const { id } = await params;
  if (!parseBillId(id)) return shareImageNotFound();
  try {
    const { bill } = await getBill(id);
    return await shareImageResponse(billPreviewCopy(bill), BILL_IMAGE_CACHE_CONTROL);
  } catch (err) {
    if (err instanceof ApiError && (err.status === 404 || err.status === 400)) return shareImageNotFound();
    throw err;
  }
}
