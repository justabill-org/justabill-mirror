"use client";

import { useState, useRef, useEffect } from "react";
import type { Bill, BillCardFacts, BillSummary, UserVoteChoice } from "@/lib/types";
import { BILL_TYPE_LABELS } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { BillLinkShareSheet } from "@/components/share/bill-link-share";
import { billLabel } from "@/lib/share-links";
import { CardFactRows, CardLead, CardWhoItAffects } from "./vote-card-facts";
import { VoteDetails } from "./vote-details";

/** The way the card leaves for each vote, the same as its swipe. */
const VOTE_EXIT = { yea: "right", nay: "left", skip: "up" } as const;

interface SwipeCardProps {
  bill: Bill;
  /** The AI summary: the card leads with it when there is one. */
  summary?: BillSummary | null;
  /** How it passed, when it became law and the CRS lead (#662); null or absent without them. */
  card?: BillCardFacts | null;
  onVote: (vote: UserVoteChoice) => void;
  isAnimating?: boolean;
}

export function SwipeCard({ bill, summary, card, onVote, isAnimating }: SwipeCardProps) {
  const [showDetails, setShowDetails] = useState(false);
  // The bill's Share sheet (#811): while it's open the card neither drags nor votes.
  const [sharing, setSharing] = useState(false);
  const paused = showDetails || sharing;
  const [swipeDirection, setSwipeDirection] = useState<"left" | "right" | "up" | null>(null);
  const cardRef = useRef<HTMLDivElement>(null);
  const startX = useRef(0);
  const startY = useRef(0);
  const currentX = useRef(0);
  const currentY = useRef(0);
  const isDragging = useRef(false);

  const typeLabel = BILL_TYPE_LABELS[bill.bill_type] || bill.bill_type.toUpperCase();

  // Handle touch/mouse events for swipe
  const handleStart = (clientX: number, clientY: number) => {
    if (isAnimating || paused) return;
    startX.current = clientX;
    startY.current = clientY;
    currentX.current = 0;
    currentY.current = 0;
    isDragging.current = true;
  };

  const handleMove = (clientX: number, clientY: number) => {
    if (!isDragging.current || isAnimating || paused) return;
    const deltaX = clientX - startX.current;
    const deltaY = startY.current - clientY;

    currentX.current = deltaX;
    currentY.current = deltaY;

    if (cardRef.current) {
      const rotation = deltaX * 0.05;
      cardRef.current.style.transform = `translateX(${deltaX}px) translateY(${-deltaY * 0.3}px) rotate(${rotation}deg)`;

      if (Math.abs(deltaX) > 50) {
        setSwipeDirection(deltaX > 0 ? "right" : "left");
      } else if (deltaY > 50) {
        setSwipeDirection("up");
      } else {
        setSwipeDirection(null);
      }
    }
  };

  const handleEnd = () => {
    if (!isDragging.current || isAnimating || paused) return;
    isDragging.current = false;

    const threshold = 100;

    if (currentX.current > threshold) {
      animateOut("right");
      onVote("yea");
    } else if (currentX.current < -threshold) {
      animateOut("left");
      onVote("nay");
    } else if (currentY.current > threshold) {
      animateOut("up");
      onVote("skip");
    } else {
      if (cardRef.current) {
        cardRef.current.style.transform = "";
      }
      setSwipeDirection(null);
    }
  };

  const animateOut = (direction: "left" | "right" | "up") => {
    if (!cardRef.current) return;

    const card = cardRef.current;
    card.style.transition = "transform 0.3s ease-out, opacity 0.3s ease-out";

    if (direction === "left") {
      card.style.transform = "translateX(-150%) rotate(-20deg)";
    } else if (direction === "right") {
      card.style.transform = "translateX(150%) rotate(20deg)";
    } else {
      card.style.transform = "translateY(-150%)";
    }

    card.style.opacity = "0";
  };

  // Reset card visual styles when bill changes (DOM-only, no state)
  useEffect(() => {
    if (cardRef.current) {
      cardRef.current.style.transition = "";
      cardRef.current.style.transform = "";
      cardRef.current.style.opacity = "1";
    }
  }, [bill.id]);


  return (
    <>
      <div className="relative mx-auto max-w-lg">
        {/* Direction indicators */}
        <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-between px-4">
          <div
            className={`rounded-full bg-vote-nay/90 px-4 py-2 font-bold text-vote-nay-foreground transition-opacity ${
              swipeDirection === "left" ? "opacity-100" : "opacity-0"
            }`}
          >
            NAY
          </div>
          <div
            className={`rounded-full bg-vote-yea/90 px-4 py-2 font-bold text-vote-yea-foreground transition-opacity ${
              swipeDirection === "right" ? "opacity-100" : "opacity-0"
            }`}
          >
            YEA
          </div>
        </div>
        <div
          className={`pointer-events-none absolute inset-x-0 top-4 z-10 flex justify-center transition-opacity ${
            swipeDirection === "up" ? "opacity-100" : "opacity-0"
          }`}
        >
          <div className="rounded-full bg-vote-skip/90 px-4 py-2 font-bold text-vote-skip-foreground">SKIP</div>
        </div>

        {/* Card */}
        <Card
          ref={cardRef}
          className="cursor-grab touch-none select-none active:cursor-grabbing"
          onMouseDown={(e) => handleStart(e.clientX, e.clientY)}
          onMouseMove={(e) => handleMove(e.clientX, e.clientY)}
          onMouseUp={handleEnd}
          onMouseLeave={handleEnd}
          onTouchStart={(e) => handleStart(e.touches[0].clientX, e.touches[0].clientY)}
          onTouchMove={(e) => handleMove(e.touches[0].clientX, e.touches[0].clientY)}
          onTouchEnd={handleEnd}
        >
          <div className="flex flex-col gap-3 p-4 sm:p-5">
            {/* Header: the number never wraps; the policy area gives way first */}
            <div className="flex items-center justify-between gap-2">
              <div className="flex min-w-0 items-center gap-2">
                <span className="shrink-0 whitespace-nowrap text-base font-semibold tabular-nums text-foreground">
                  {typeLabel}{"\u00a0"}{bill.number}
                </span>
                {bill.policy_area && (
                  <Badge variant="secondary" className="min-w-0 text-xs">
                    <span className="truncate">{bill.policy_area}</span>
                  </Badge>
                )}
              </div>
              <div className="-mr-2 flex shrink-0 items-center">
                {/* Pressing Share never starts a drag: the press stops here, before the card sees it. */}
                <Button
                  variant="ghost"
                  size="sm"
                  onMouseDown={(e) => e.stopPropagation()}
                  onTouchStart={(e) => e.stopPropagation()}
                  onClick={(e) => {
                    e.stopPropagation();
                    setSharing(true);
                  }}
                  aria-haspopup="dialog"
                  aria-label={`Share ${billLabel(bill.id)}`}
                  className="text-muted-foreground hover:text-foreground"
                >
                  <ShareIcon className="mr-1 h-4 w-4" />
                  Share
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={(e) => {
                    e.stopPropagation();
                    setShowDetails(true);
                  }}
                  aria-haspopup="dialog"
                  className="text-muted-foreground hover:text-foreground"
                >
                  <InfoIcon className="mr-1 h-4 w-4" />
                  Details
                </Button>
              </div>
            </div>

            {/* Title */}
            <h2 className="text-base font-semibold leading-snug text-foreground line-clamp-2 sm:text-lg">
              {bill.title}
            </h2>

            {/* What it does, labeled with its source */}
            <CardLead summary={summary} card={card} />
            <CardWhoItAffects summary={summary} />

            {/* How it passed, when it became law, the sponsor */}
            <CardFactRows bill={bill} card={card} />

            {/* Vote buttons */}
            <div className="pt-1">
              <div className="flex items-center gap-2">
                <Button
                  size="lg"
                  variant="outline"
                  className="flex-1 border-vote-nay/50 text-vote-nay hover:bg-vote-nay hover:text-vote-nay-foreground hover:border-vote-nay"
                  onClick={(e) => {
                    e.stopPropagation();
                    animateOut("left");
                    onVote("nay");
                  }}
                >
                  <ThumbsDownIcon className="mr-2 h-5 w-5 shrink-0" />
                  Nay
                </Button>
                <Button
                  size="lg"
                  variant="outline"
                  className="flex-1 border-vote-skip/50 text-vote-skip hover:bg-vote-skip hover:text-vote-skip-foreground hover:border-vote-skip"
                  onClick={(e) => {
                    e.stopPropagation();
                    animateOut("up");
                    onVote("skip");
                  }}
                >
                  Skip
                </Button>
                <Button
                  size="lg"
                  variant="outline"
                  className="flex-1 border-vote-yea/50 text-vote-yea hover:bg-vote-yea hover:text-vote-yea-foreground hover:border-vote-yea"
                  onClick={(e) => {
                    e.stopPropagation();
                    animateOut("right");
                    onVote("yea");
                  }}
                >
                  <ThumbsUpIcon className="mr-2 h-5 w-5 shrink-0" />
                  Yea
                </Button>
              </div>
              <p className="mt-2 text-center text-xs text-muted-foreground">
                Swipe right for Yea, left for Nay, up to Skip
              </p>
            </div>
          </div>
        </Card>
      </div>

      <VoteDetails
        bill={bill}
        summary={summary}
        card={card}
        open={showDetails}
        onOpenChange={setShowDetails}
        onVote={(vote) => {
          animateOut(VOTE_EXIT[vote]);
          onVote(vote);
        }}
      />

      {/* Outside the card, so the card's drag handlers and select-none never reach the sheet. */}
      {sharing && <BillLinkShareSheet billId={bill.id} title={bill.title} onClose={() => setSharing(false)} />}
    </>
  );
}

function ShareIcon({ className }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2} aria-hidden="true">
      <path strokeLinecap="round" strokeLinejoin="round" d="M9 8.25H7.5a2.25 2.25 0 0 0-2.25 2.25v9a2.25 2.25 0 0 0 2.25 2.25h9a2.25 2.25 0 0 0 2.25-2.25v-9a2.25 2.25 0 0 0-2.25-2.25H15m0-3-3-3m0 0-3 3m3-3V15" />
    </svg>
  );
}

function InfoIcon({ className }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M11.25 11.25l.041-.02a.75.75 0 011.063.852l-.708 2.836a.75.75 0 001.063.853l.041-.021M21 12a9 9 0 11-18 0 9 9 0 0118 0zm-9-3.75h.008v.008H12V8.25z" />
    </svg>
  );
}

function ThumbsUpIcon({ className }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.75} aria-hidden="true">
      <path strokeLinecap="round" strokeLinejoin="round" d="M6.633 10.25c.806 0 1.533-.446 2.031-1.08a9.041 9.041 0 0 1 2.861-2.4c.723-.384 1.35-.956 1.653-1.715a4.498 4.498 0 0 0 .322-1.672V2.75a.75.75 0 0 1 .75-.75 2.25 2.25 0 0 1 2.25 2.25c0 1.152-.26 2.243-.723 3.218-.266.558.107 1.282.725 1.282m0 0h3.126c1.026 0 1.945.694 2.054 1.715.045.422.068.85.068 1.285a11.95 11.95 0 0 1-2.649 7.521c-.388.482-.987.729-1.605.729H13.48c-.483 0-.964-.078-1.423-.23l-3.114-1.04a4.501 4.501 0 0 0-1.423-.23H5.904m10.598-9.75H14.25M5.904 18.5c.083.205.173.405.27.602.197.4-.078.898-.523.898h-.908c-.889 0-1.713-.518-1.972-1.368a12 12 0 0 1-.521-3.507c0-1.553.295-3.036.831-4.398C3.387 9.953 4.167 9.5 5 9.5h1.053c.472 0 .745.556.5.96a8.958 8.958 0 0 0-1.302 4.665c0 1.194.232 2.333.654 3.375Z" />
    </svg>
  );
}

function ThumbsDownIcon({ className }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.75} aria-hidden="true">
      <path strokeLinecap="round" strokeLinejoin="round" d="M7.498 15.25H4.372c-1.026 0-1.945-.694-2.054-1.715a12.137 12.137 0 0 1-.068-1.285c0-2.848.992-5.464 2.649-7.521C5.287 4.247 5.886 4 6.504 4h4.016a4.5 4.5 0 0 1 1.423.23l3.114 1.04a4.5 4.5 0 0 0 1.423.23h1.294M7.498 15.25c.618 0 .991.724.725 1.282A7.471 7.471 0 0 0 7.5 19.75 2.25 2.25 0 0 0 9.75 22a.75.75 0 0 0 .75-.75v-.633c0-.573.11-1.14.322-1.672.304-.76.93-1.33 1.653-1.715a9.04 9.04 0 0 0 2.86-2.4c.498-.634 1.226-1.08 2.032-1.08h.384m-10.253 1.5H9.7m8.075-9.75c.01.05.027.1.05.148.593 1.2.925 2.55.925 3.977 0 1.487-.36 2.89-.999 4.125m.023-8.25c-.076-.365.183-.75.575-.75h.908c.889 0 1.713.518 1.972 1.368.339 1.11.521 2.287.521 3.507 0 1.553-.295 3.036-.831 4.398-.306.774-1.086 1.227-1.918 1.227h-1.053c-.472 0-.745-.556-.5-.96a8.95 8.95 0 0 0 .303-.54" />
    </svg>
  );
}
