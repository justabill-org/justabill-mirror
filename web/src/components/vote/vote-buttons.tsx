"use client";

import { useState } from "react";
import type { UserVoteChoice } from "@/lib/types";

interface VoteButtonsProps {
  billId: string;
  currentVote?: UserVoteChoice;
  onVote: (vote: UserVoteChoice) => void | Promise<void>;
  size?: "sm" | "md" | "lg";
  disabled?: boolean;
}

export function VoteButtons({
  currentVote,
  onVote,
  size = "md",
  disabled = false,
}: VoteButtonsProps) {
  const [isLoading, setIsLoading] = useState(false);
  const [pendingVote, setPendingVote] = useState<UserVoteChoice | undefined>();
  // The click shows at once while it's saved; after that the stored vote (which can also change
  // on hydration or in another tab) is the truth, so a failed save falls back to it.
  const activeVote = pendingVote ?? currentVote;

  const handleVote = async (vote: UserVoteChoice) => {
    if (disabled || isLoading) return;

    setIsLoading(true);
    setPendingVote(vote);
    try {
      await onVote(vote);
    } finally {
      setPendingVote(undefined);
      setIsLoading(false);
    }
  };

  const sizes = {
    sm: "h-9 px-4 text-sm",
    md: "h-11 px-6 text-base",
    lg: "h-14 px-8 text-lg",
  };

  const baseStyles = `${sizes[size]} font-semibold rounded-xl transition-all focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-offset-2 disabled:opacity-50`;

  return (
    <div className="flex items-center gap-3">
      <button
        onClick={() => handleVote("yea")}
        disabled={disabled || isLoading}
        className={`${baseStyles} ${
          activeVote === "yea"
            ? "bg-vote-yea text-vote-yea-foreground ring-2 ring-vote-yea ring-offset-2"
            : "bg-vote-yea/10 text-vote-yea hover:bg-vote-yea hover:text-vote-yea-foreground"
        }`}
        aria-pressed={activeVote === "yea"}
      >
        Yea
      </button>

      <button
        onClick={() => handleVote("nay")}
        disabled={disabled || isLoading}
        className={`${baseStyles} ${
          activeVote === "nay"
            ? "bg-vote-nay text-vote-nay-foreground ring-2 ring-vote-nay ring-offset-2"
            : "bg-vote-nay/10 text-vote-nay hover:bg-vote-nay hover:text-vote-nay-foreground"
        }`}
        aria-pressed={activeVote === "nay"}
      >
        Nay
      </button>

      <button
        onClick={() => handleVote("skip")}
        disabled={disabled || isLoading}
        className={`${baseStyles} ${
          activeVote === "skip"
            ? "bg-vote-skip text-vote-skip-foreground ring-2 ring-vote-skip ring-offset-2"
            : "bg-vote-skip/10 text-vote-skip hover:bg-vote-skip hover:text-vote-skip-foreground"
        }`}
        aria-pressed={activeVote === "skip"}
      >
        Skip
      </button>
    </div>
  );
}
