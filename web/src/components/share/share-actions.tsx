"use client";

// The share actions (#88, #810): the link in a read-only field, Share… (only where the browser can
// share natively), Copy link, links to each site's share page, and a status line. The card dialog
// and the bill's own share sheet both render it, so they offer the same ways to share. Nothing is
// sent until the visitor picks one, and then only the link and its text.

import { useId, useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { trackShare } from "@/lib/analytics";
import {
  intentLinks,
  isAbortError,
  shareSupport,
  type ShareChannel,
  type ShareKind,
  type ShareNavigator,
} from "@/lib/share-links";

export const COPY_FAILED_MESSAGE = "Couldn't copy the link. Select it above and copy it instead.";
export const NATIVE_FAILED_MESSAGE = "Sharing didn't work here. Copy the link instead.";

export interface ShareActionsProps {
  kind: ShareKind;
  /** The absolute link to share, or null while it's being worked out (no field or links yet). */
  url: string | null;
  /** The words that go with the link. */
  text: string;
  /** Turns off Share… and Copy link, e.g. while the card it links to can't be shown. */
  disabled?: boolean;
  /** Shared along with the link by Share… when the browser can share files (the card image). */
  file?: File | null;
  /** More buttons after Copy link, such as the card's Download image. */
  children?: ReactNode;
}

function browserNavigator(): ShareNavigator | undefined {
  return typeof navigator === "undefined" ? undefined : navigator;
}

export function ShareActions({ kind, url, text, disabled = false, file = null, children }: ShareActionsProps) {
  const [message, setMessage] = useState<string | null>(null);
  const linkId = useId();
  const nav = browserNavigator();
  const canShareNatively = shareSupport(nav, null) !== "none";
  const usable = url !== null && !disabled;

  const sent = (channel: ShareChannel) => trackShare(kind, channel);

  // No await before navigator.share, so the click's user activation still holds.
  const handleShare = () => {
    if (!url || !nav?.share) return;
    setMessage(null);
    const data: ShareData = shareSupport(nav, file) === "files" && file ? { files: [file], url, text } : { url, text };
    nav.share(data).then(
      () => sent("native"),
      (err: unknown) => {
        if (!isAbortError(err)) setMessage(NATIVE_FAILED_MESSAGE);
      }
    );
  };

  // In-app browsers and denied permissions have no clipboard (or one that throws): say how to copy.
  const handleCopy = async () => {
    if (!url) return;
    try {
      await navigator.clipboard.writeText(url);
      setMessage("Link copied.");
      sent("copy");
    } catch {
      setMessage(COPY_FAILED_MESSAGE);
    }
  };

  return (
    <>
      {url && (
        <div className="space-y-1">
          <label htmlFor={linkId} className="text-sm font-medium text-foreground">
            Link
          </label>
          <input
            id={linkId}
            readOnly
            value={url}
            onFocus={(e) => e.currentTarget.select()}
            className="h-10 w-full rounded-lg border border-border bg-muted px-3 text-sm text-foreground"
          />
        </div>
      )}

      <div className="flex flex-wrap gap-2">
        {canShareNatively && (
          <Button onClick={handleShare} disabled={!usable}>
            Share…
          </Button>
        )}
        <Button variant="outline" onClick={handleCopy} disabled={!usable}>
          Copy link
        </Button>
        {children}
      </div>

      {url && usable && (
        <ul className="flex flex-wrap gap-x-4 gap-y-2 text-sm" aria-label="Share on">
          {intentLinks(url, text).map((link) => (
            <li key={link.channel}>
              <a
                href={link.href}
                target="_blank"
                rel="noopener noreferrer"
                onClick={() => sent(link.channel)}
                className="font-medium text-foreground underline underline-offset-4 hover:text-link"
              >
                {link.label}
              </a>
            </li>
          ))}
        </ul>
      )}

      <p role="status" className="min-h-5 text-sm text-foreground">
        {message}
      </p>
    </>
  );
}
