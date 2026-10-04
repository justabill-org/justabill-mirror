"use client";

import { useId, useState } from "react";
import type { BillAction } from "@/lib/types";
import { mergeActions } from "@/lib/bill-actions";
import { Button } from "@/components/ui/button";
import { formatDate } from "@/lib/utils";

interface ActionTimelineProps {
  actions: BillAction[];
}

/**
 * The bill's actions, newest first (#664). The same action from several sources shows once, with
 * each source as a tag. The key actions (Congress.gov's overview of major actions, and roll-call
 * votes) show first; "Show all" lists the rest.
 */
export function ActionTimeline({ actions }: ActionTimelineProps) {
  const [showAll, setShowAll] = useState(false);
  const listId = useId();
  const merged = mergeActions(actions);

  if (merged.length === 0) {
    return <p className="text-sm text-muted-foreground">No actions recorded yet.</p>;
  }

  const key = merged.filter((a) => a.isKey);
  // Without an overview from Congress.gov, there's nothing to pick from: show everything.
  const hasKey = key.length > 0 && key.length < merged.length;
  const shown = showAll || !hasKey ? merged : key;

  return (
    <div className="space-y-4">
      {hasKey && (
        <p className="text-sm text-muted-foreground">
          {showAll
            ? `All ${merged.length} actions, each listed once with every source that recorded it.`
            : `${key.length} key actions of ${merged.length}: Congress.gov's major actions and roll-call votes.`}
        </p>
      )}

      <div className="relative">
        <div aria-hidden="true" className="absolute left-1.5 top-2 bottom-2 w-px bg-border sm:left-3" />
        <ol id={listId} className="space-y-5">
          {shown.map((action, index) => (
            <li key={action.key} className="relative pl-6 sm:pl-8">
              <span
                aria-hidden="true"
                className={`absolute left-0 top-1.5 h-3 w-3 sm:left-1.5 rounded-full border-2 ${
                  index === 0 ? "border-foreground bg-foreground" : "border-border bg-background"
                }`}
              />
              <time dateTime={action.date.slice(0, 10)} className="text-sm font-medium text-foreground">
                {formatDate(action.date)}
              </time>
              <p className="mt-0.5 text-sm leading-relaxed text-foreground">{action.text}</p>
              <p className="mt-1 text-xs text-muted-foreground">
                <span className="sr-only">Recorded by: </span>
                {action.sources.join(" · ")}
              </p>
              {action.recordedVote && (
                <a
                  href={action.recordedVote.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="mt-1 inline-block py-1 text-xs text-link underline underline-offset-2"
                >
                  Roll call {action.recordedVote.roll_number} ({action.recordedVote.chamber})
                </a>
              )}
            </li>
          ))}
        </ol>
      </div>

      {hasKey && (
        <Button
          variant="outline"
          size="sm"
          aria-expanded={showAll}
          aria-controls={listId}
          onClick={() => setShowAll(!showAll)}
        >
          {showAll ? "Show key actions only" : `Show all ${merged.length} actions`}
        </Button>
      )}
    </div>
  );
}
