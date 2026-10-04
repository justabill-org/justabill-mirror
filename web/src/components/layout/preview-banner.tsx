import { previewExamplesOn } from "@/lib/fallback";

type Env = Record<string, string | undefined>;

/**
 * The notice at the top of every page of a Vercel preview while previews show example data (#678;
 * see previewExamplesOn). The API is private until go-public, so every data page on a preview shows
 * the fixtures. Renders nothing in production and under `next dev`.
 */
export function PreviewBanner({ env = process.env }: { env?: Env }) {
  if (!previewExamplesOn(env)) return null;
  return (
    <aside
      aria-label="Preview notice"
      className="border-b border-border bg-muted px-4 py-2 text-center text-sm font-medium text-foreground"
    >
      Preview: example data. The API opens to previews at go-public.
    </aside>
  );
}
