import type { CongressionalVote } from "@/lib/types";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { formatDate } from "@/lib/utils";

interface CongressionalVotesProps {
  votes: CongressionalVote[];
}

/** How a measure passed without a roll call, from the method segment of its vote ID. */
type UnrecordedMethod = "voice" | "uc" | "unknown";

/**
 * A vote with no roll number records no individual positions: the pipeline writes one for each
 * passage by voice vote or unanimous consent, with an ID like
 * `house-119-voice-hr-119-1276-20251209` or `senate-119-uc-s-119-858-20260807`.
 */
function unrecordedMethod(vote: CongressionalVote): UnrecordedMethod | null {
  if (vote.roll_number != null) return null;
  const method = vote.id.split("-")[2];
  return method === "voice" || method === "uc" ? method : "unknown";
}

const UNRECORDED_LABELS: Record<UnrecordedMethod, { short: string; passed: string }> = {
  voice: { short: "Voice Vote", passed: "Passed by voice vote" },
  uc: { short: "Unanimous Consent", passed: "Passed by unanimous consent" },
  unknown: { short: "No Recorded Vote", passed: "Passed without a recorded vote" },
};

export function CongressionalVotes({ votes }: CongressionalVotesProps) {
  if (votes.length === 0) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="text-lg">Congressional votes</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            No congressional votes recorded yet.
          </p>
        </CardContent>
      </Card>
    );
  }

  return (
    <div className="space-y-4">
      {votes.map((vote) => (
        <VoteCard key={vote.id} vote={vote} />
      ))}
    </div>
  );
}

function VoteCard({ vote }: { vote: CongressionalVote }) {
  const method = unrecordedMethod(vote);
  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between gap-2">
          <CardTitle className="text-base">
            {vote.chamber} Vote
            {method ? (
              <span className="ml-2 text-muted-foreground font-normal">
                {UNRECORDED_LABELS[method].short}
              </span>
            ) : vote.roll_number ? (
              <span className="ml-2 text-muted-foreground font-normal">
                Roll #{vote.roll_number}
              </span>
            ) : null}
          </CardTitle>
          {vote.result && (
            <Badge
              variant={
                vote.result.toLowerCase().includes("passed") ||
                vote.result.toLowerCase().includes("agreed")
                  ? "success"
                  : "secondary"
              }
            >
              {vote.result}
            </Badge>
          )}
        </div>
      </CardHeader>
      <CardContent>
        {method ? (
          <UnrecordedExplanation chamber={vote.chamber} method={method} question={vote.question} />
        ) : (
          <RollCallDetails vote={vote} />
        )}

        {/* Date */}
        <p className="mt-4 text-xs text-muted-foreground">
          {formatDate(vote.vote_date, "weekday")}
        </p>
      </CardContent>
    </Card>
  );
}

const UNRECORDED_DESCRIPTIONS: Record<UnrecordedMethod, (chamber: string) => string> = {
  voice: (chamber) =>
    `The ${chamber} passed this measure by voice vote: members present answered aloud together, and ` +
    "nobody asked for a roll call.",
  uc: (chamber) =>
    `The ${chamber} passed this measure by unanimous consent: it was proposed and no member present objected.`,
  unknown: (chamber) => `The ${chamber} passed this measure without taking a roll call.`,
};

/** The pipeline stores these rows' action text as the question, after a method prefix. */
function actionText(question?: string): string | undefined {
  const text = question?.replace(/^(Voice Vote|Unanimous Consent):\s*/, "").trim();
  return text || undefined;
}

function UnrecordedExplanation({
  chamber,
  method,
  question,
}: {
  chamber: string;
  method: UnrecordedMethod;
  question?: string;
}) {
  const action = actionText(question);
  return (
    <div className="rounded-md bg-muted/50 p-4">
      <p className="text-sm text-foreground font-medium mb-1">
        {UNRECORDED_LABELS[method].passed}
      </p>
      <p className="text-sm text-muted-foreground">
        {UNRECORDED_DESCRIPTIONS[method](chamber)} No individual votes were recorded, so this vote
        isn&apos;t part of anyone&apos;s scorecard.
      </p>
      {action && <p className="mt-2 text-xs text-muted-foreground">Floor action: {action}</p>}
    </div>
  );
}

function RollCallDetails({ vote }: { vote: CongressionalVote }) {
  return (
    <>
      {vote.question && (
        <p className="text-sm text-muted-foreground mb-4">{vote.question}</p>
      )}

      {/* Vote counts */}
      <div className="flex flex-wrap gap-4">
        {vote.yeas !== undefined && vote.yeas > 0 && (
          <VoteCount label="Yeas" count={vote.yeas} variant="yea" />
        )}
        {vote.nays !== undefined && vote.nays > 0 && (
          <VoteCount label="Nays" count={vote.nays} variant="nay" />
        )}
        {vote.present !== undefined && vote.present > 0 && (
          <VoteCount label="Present" count={vote.present} variant="neutral" />
        )}
        {vote.not_voting !== undefined && vote.not_voting > 0 && (
          <VoteCount label="Not Voting" count={vote.not_voting} variant="neutral" />
        )}
      </div>

      {/* Vote bar */}
      {vote.yeas !== undefined && vote.nays !== undefined && (vote.yeas + vote.nays) > 0 && (
        <div className="mt-4">
          <div className="flex h-3 overflow-hidden rounded-full bg-muted">
            <div
              className="bg-vote-yea transition-all"
              style={{
                width: `${(vote.yeas / (vote.yeas + vote.nays)) * 100}%`,
              }}
            />
            <div
              className="bg-vote-nay transition-all"
              style={{
                width: `${(vote.nays / (vote.yeas + vote.nays)) * 100}%`,
              }}
            />
          </div>
        </div>
      )}
    </>
  );
}

function VoteCount({
  label,
  count,
  variant,
}: {
  label: string;
  count: number;
  variant: "yea" | "nay" | "neutral";
}) {
  const colors = {
    yea: "text-vote-yea",
    nay: "text-vote-nay",
    neutral: "text-muted-foreground",
  };

  return (
    <div className="flex items-baseline gap-2">
      <span className={`text-2xl font-bold ${colors[variant]}`}>{count}</span>
      <span className="text-sm text-muted-foreground">{label}</span>
    </div>
  );
}
