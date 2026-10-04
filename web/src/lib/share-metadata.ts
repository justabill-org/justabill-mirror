import type { Metadata } from "next";
import { SHARE_IMAGE_HEIGHT, SHARE_IMAGE_WIDTH, shareImageUrl, type CardCopy } from "./share";

/**
 * Metadata for a share page: the card as its Open Graph and X image, with alt text that repeats
 * the card's words. Share pages are personal statements, so they're noindex (but not disallowed
 * in robots.txt, or link-preview crawlers that honor it would show no preview).
 */
export function shareMetadata(pageUrl: string, copy: CardCopy): Metadata {
  const title = copy.summary;
  const image = {
    url: shareImageUrl(pageUrl),
    width: SHARE_IMAGE_WIDTH,
    height: SHARE_IMAGE_HEIGHT,
    alt: copy.plain,
  };
  return {
    title: `${title} | Just a Bill`,
    description: copy.plain,
    robots: { index: false, follow: true },
    openGraph: {
      title,
      description: copy.plain,
      type: "website",
      url: pageUrl,
      siteName: "Just a Bill",
      images: [image],
    },
    twitter: {
      card: "summary_large_image",
      title,
      description: copy.plain,
      images: [image],
    },
  };
}
