"use client";

// The share dialog (#88, docs/design/88-share-cards.md, item 2). It shows the exact card image and
// link, and shares them only when the visitor picks an action. The PNG is fetched when the dialog
// opens, not on click: navigator.share() needs the click's user activation, which Safari loses if
// the handler waits for a fetch first. Only the share path leaves the browser, and it holds only
// what the card prints. The link field and the share buttons are ShareActions, shared with the
// bill's own share sheet (#810).

import { useEffect, useState, type ReactNode } from "react";
import { buttonClasses } from "@/components/ui/button";
import { Sheet, SheetClose, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { trackShare } from "@/lib/analytics";
import { shareImageUrl, SHARE_IMAGE_HEIGHT, SHARE_IMAGE_WIDTH } from "@/lib/share";
import { absoluteShareUrl, shareFileName, type ShareKind } from "@/lib/share-links";
import { ShareActions } from "./share-actions";

export const SHARE_PRIVACY_NOTE =
  "Anyone with this link sees this card. It doesn't include your other votes or your address.";

/** The aggregate card's note (#166): it holds no vote of yours, but the place may be your own. */
export const AGGREGATE_SHARE_PRIVACY_NOTE =
  "Anyone with this link sees this card. It doesn't include your vote or your address, but it names the " +
  "place, so if that's your own state or district, the link shows it.";

/** The card image for one share path: loaded, missing (the card would 404), or failed to load. */
type CardImage =
  | { path: string; status: "ready"; blob: Blob; src: string }
  | { path: string; status: "missing" }
  | { path: string; status: "error" };

export interface ShareDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  kind: ShareKind;
  /** The share page path, e.g. /share/bill/hr-119-1/yea, or null while it's being worked out. */
  path: string | null;
  /** The words that go with the link: only what the card prints. */
  text: string;
  /** Controls above the preview, such as the bill card's member picker. */
  children?: ReactNode;
  /** What the link discloses, under the title. */
  note?: string;
}

/** Fetches the card image for `path` while the dialog is open, and frees it when that changes. */
function useCardImage(open: boolean, path: string | null): CardImage | null {
  const [image, setImage] = useState<CardImage | null>(null);

  useEffect(() => {
    if (!open || !path) return;
    let cancelled = false;
    let src: string | null = null;
    fetch(shareImageUrl(path))
      .then(async (res) => {
        if (res.status === 404) {
          if (!cancelled) setImage({ path, status: "missing" });
          return;
        }
        if (!res.ok) throw new Error(`share image: HTTP ${res.status}`);
        const blob = await res.blob();
        if (cancelled) return;
        src = URL.createObjectURL(blob);
        setImage({ path, status: "ready", blob, src });
      })
      .catch(() => {
        if (!cancelled) setImage({ path, status: "error" });
      });
    return () => {
      cancelled = true;
      if (src) URL.revokeObjectURL(src);
    };
  }, [open, path]);

  return open && image?.path === path ? image : null;
}

export function ShareDialog({
  open,
  onOpenChange,
  kind,
  path,
  text,
  children,
  note = SHARE_PRIVACY_NOTE,
}: ShareDialogProps) {
  const image = useCardImage(open, path);

  if (!open) return null;

  const url = path && typeof window !== "undefined" ? absoluteShareUrl(path, window.location.origin) : null;
  const fileName = path ? shareFileName(path) : "just-a-bill.png";
  const file = image?.status === "ready" ? new File([image.blob], fileName, { type: "image/png" }) : null;

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="mx-auto max-h-[90vh] max-w-lg overflow-y-auto rounded-t-xl pb-6">
        <SheetHeader className="pr-12">
          <SheetTitle>Share this card</SheetTitle>
          <SheetDescription>{note}</SheetDescription>
          <SheetClose onClick={() => onOpenChange(false)} />
        </SheetHeader>

        <div className="space-y-4 px-6 pt-4">
          {children}

          <CardPreview image={image} alt={text} waiting={!path} />

          <ShareActions
            kind={kind}
            url={url}
            text={text}
            disabled={image === null || image.status === "missing"}
            file={file}
          >
            {image?.status === "ready" && (
              <a
                href={image.src}
                download={fileName}
                onClick={() => trackShare(kind, "download")}
                className={buttonClasses({ variant: "outline" })}
              >
                Download image
              </a>
            )}
          </ShareActions>
        </div>
      </SheetContent>
    </Sheet>
  );
}

function CardPreview({ image, alt, waiting }: { image: CardImage | null; alt: string; waiting: boolean }) {
  if (waiting || image === null) {
    return <Skeleton className="aspect-[1200/630] w-full rounded-lg" role="status" aria-label="Making your card" />;
  }
  if (image.status === "missing") {
    return (
      <p role="alert" className="text-sm text-destructive">
        This card isn&apos;t available. The bill or member may not be in our records yet.
      </p>
    );
  }
  if (image.status === "error") {
    return (
      <p role="alert" className="text-sm text-destructive">
        The card image didn&apos;t load. You can still share the link.
      </p>
    );
  }
  return (
    // A blob: URL of the image already fetched, so next/image has nothing to optimize.
    // eslint-disable-next-line @next/next/no-img-element
    <img
      src={image.src}
      alt={alt}
      width={SHARE_IMAGE_WIDTH}
      height={SHARE_IMAGE_HEIGHT}
      className="h-auto w-full rounded-lg border border-border"
    />
  );
}
