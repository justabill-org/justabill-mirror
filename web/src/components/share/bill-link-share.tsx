"use client";

// "Share" on a bill (#786, #810): a sheet that shares the bill's own page, with no card image.
// The link is always the bill's canonical URL on the site's origin, whatever page it's opened
// from, so a list's filters or an experiment arm (/vote/v/treatment) never leave the browser.
// It reads nothing at render, so the pages it sits on stay cacheable.

import { useState } from "react";
import { Button, type ButtonSize, type ButtonVariant } from "@/components/ui/button";
import { Sheet, SheetClose, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { billLabel, billLinkText, billLinkUrl } from "@/lib/share-links";
import { ShareActions } from "./share-actions";

export const BILL_LINK_PRIVACY_NOTE =
  "The link goes to this bill's page. It doesn't include your vote or anything else about you.";

export interface BillLinkShareButtonProps {
  billId: string;
  title: string;
  size?: ButtonSize;
  variant?: ButtonVariant;
  className?: string;
}

export function BillLinkShareButton({
  billId,
  title,
  size = "sm",
  variant = "outline",
  className,
}: BillLinkShareButtonProps) {
  const [open, setOpen] = useState(false);
  const label = billLabel(billId);
  return (
    <>
      <Button
        variant={variant}
        size={size}
        className={className}
        onClick={() => setOpen(true)}
        aria-label={`Share ${label}`}
      >
        Share
      </Button>
      {open && <BillLinkShareSheet billId={billId} title={title} onClose={() => setOpen(false)} />}
    </>
  );
}

/**
 * The sheet alone, for a caller that renders its own Share button: the /vote card keeps it outside
 * the card it drags, so a drag never starts inside the sheet and its link field stays selectable.
 */
export function BillLinkShareSheet({ billId, title, onClose }: { billId: string; title: string; onClose: () => void }) {
  const url = billLinkUrl(billId, window.location.origin);
  const label = billLabel(billId);
  return (
    <Sheet open onOpenChange={(next) => !next && onClose()}>
      <SheetContent className="mx-auto max-h-[90vh] max-w-lg overflow-y-auto rounded-t-xl pb-6">
        <SheetHeader className="pr-12">
          <SheetTitle>Share this bill</SheetTitle>
          {/* A title can run past 500 characters: three lines here, the whole of it on the bill's page. */}
          <SheetDescription className="line-clamp-3 text-foreground">
            <span className="font-medium tabular-nums">{label}</span>
            {title.trim() && `: ${title}`}
          </SheetDescription>
          <p className="text-sm text-muted-foreground">{BILL_LINK_PRIVACY_NOTE}</p>
          <SheetClose onClick={onClose} />
        </SheetHeader>

        <div className="space-y-4 px-6 pt-4">
          <ShareActions kind="link" url={url} text={billLinkText(billId, title)} />
        </div>
      </SheetContent>
    </Sheet>
  );
}
