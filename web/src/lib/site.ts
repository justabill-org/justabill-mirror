/**
 * The canonical site URL from NEXT_PUBLIC_SITE_URL (#51 sets it in production), or undefined when
 * it's unset or not an http(s) URL. When undefined, Next.js falls back to the Vercel deployment
 * URL (or localhost) for absolute metadata URLs.
 */
export function siteUrl(value = process.env.NEXT_PUBLIC_SITE_URL): URL | undefined {
  if (!value) return undefined;
  try {
    const url = new URL(value);
    return url.protocol === "https:" || url.protocol === "http:" ? url : undefined;
  } catch {
    return undefined;
  }
}
