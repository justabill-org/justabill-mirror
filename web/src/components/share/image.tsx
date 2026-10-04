// Renders a share card as a 1200×630 PNG for the /share/.../image.png route handlers (#88).

import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { ImageResponse } from "next/og";
import { ShareCardImage } from "@/components/share/card";
import type { CardCopy } from "@/lib/share";
import { SHARE_IMAGE_HEIGHT, SHARE_IMAGE_WIDTH } from "@/lib/share";
import { siteUrl } from "@/lib/site";

/**
 * CDN-only caching: a day at the edge, an hour in browsers, and a week of stale-while-revalidate.
 * These are request-time route handlers rather than ISR, so a cache-busting scan costs CPU but
 * no ISR writes (design: Cost).
 */
export const SHARE_IMAGE_CACHE_CONTROL = "public, max-age=3600, s-maxage=86400, stale-while-revalidate=604800";
/**
 * Aggregate cards (#166) print numbers the hourly aggregation job republishes, and a cell that's
 * put on hold must stop being shown, so the edge keeps them an hour rather than a day.
 */
export const AGGREGATE_IMAGE_CACHE_CONTROL = "public, max-age=600, s-maxage=3600, stale-while-revalidate=3600";
/** Unknown IDs and impossible counts are cached briefly, so a scan doesn't reach the API each time. */
export const SHARE_NOT_FOUND_CACHE_CONTROL = "public, max-age=300, s-maxage=300";

type Font = { name: string; data: Buffer; weight: 400 | 700; style: "normal" };

let fonts: Promise<Font[]> | undefined;

/** Geist Regular and Bold as TTF (ImageResponse doesn't read WOFF2), loaded once per instance. */
function loadFonts(): Promise<Font[]> {
  fonts ??= Promise.all([
    readFile(join(process.cwd(), "assets/fonts/Geist-Regular.ttf")),
    readFile(join(process.cwd(), "assets/fonts/Geist-Bold.ttf")),
  ]).then(([regular, bold]) => [
    { name: "Geist", data: regular, weight: 400, style: "normal" },
    { name: "Geist", data: bold, weight: 700, style: "normal" },
  ]);
  return fonts;
}

let logo: Promise<string> | undefined;

/**
 * The scroll mascot (public/bill-icon.jpg, as an 88px PNG for the 44px box) as a data URI, read
 * once per instance like the fonts.
 */
export function loadLogo(): Promise<string> {
  logo ??= readFile(join(process.cwd(), "assets/logo.png")).then(
    (png) => `data:image/png;base64,${png.toString("base64")}`
  );
  return logo;
}

export async function shareImageResponse(
  copy: CardCopy,
  cacheControl = SHARE_IMAGE_CACHE_CONTROL
): Promise<Response> {
  const [fontData, logoData] = await Promise.all([loadFonts(), loadLogo()]);
  return new ImageResponse(<ShareCardImage copy={copy} domain={siteUrl()?.host} logo={logoData} />, {
    width: SHARE_IMAGE_WIDTH,
    height: SHARE_IMAGE_HEIGHT,
    fonts: fontData,
    // Lowercase, so it replaces ImageResponse's default cache-control instead of joining it.
    headers: { "cache-control": cacheControl },
  });
}

export function shareImageNotFound(): Response {
  return new Response("Not found", {
    status: 404,
    headers: { "content-type": "text/plain; charset=utf-8", "cache-control": SHARE_NOT_FOUND_CACHE_CONTROL },
  });
}
