import { shareImageResponse } from "@/components/share/image";
import { SITE_CARD_ALT, SITE_CARD_COPY } from "@/lib/seo";
import { SHARE_IMAGE_HEIGHT, SHARE_IMAGE_WIDTH } from "@/lib/share";

// The site's default link-preview image (#86), rendered once at build time. Pages with their own
// (bill pages, share pages) replace it.

export const alt = SITE_CARD_ALT;
export const size = { width: SHARE_IMAGE_WIDTH, height: SHARE_IMAGE_HEIGHT };
export const contentType = "image/png";

export default function Image(): Promise<Response> {
  return shareImageResponse(SITE_CARD_COPY);
}
