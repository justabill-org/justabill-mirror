import { Badge } from "@/components/ui/badge";
import { normalizePositionVote } from "@/lib/share";

/**
 * A member's or visitor's vote as a badge: Yea for Aye or Yea, Nay for No or Nay, Present, Not
 * voting, and any other word (an impeachment trial's "Guilty") as the clerk recorded it.
 */
export function VoteBadge({ vote }: { vote: string }) {
  const position = normalizePositionVote(vote);
  if (position === "yea") {
    return (
      <Badge variant="success" className="text-xs">
        Yea
      </Badge>
    );
  }
  if (position === "nay") {
    return (
      <Badge variant="destructive" className="text-xs">
        Nay
      </Badge>
    );
  }
  return (
    <Badge variant="secondary" className="text-xs">
      {position ? POSITION_LABELS[position] : vote.toLowerCase() === "other" ? "Other" : vote}
    </Badge>
  );
}

const POSITION_LABELS = { present: "Present", not_voting: "Not voting" } as const;
