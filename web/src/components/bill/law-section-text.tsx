"use client";

import { useId, useState } from "react";
import { getLawSection } from "@/lib/api";
import { reportError } from "@/lib/obs/browser";
import type { LawSectionResponse } from "@/lib/types";
import { Button } from "@/components/ui/button";

interface LawSectionTextProps {
  title: number;
  section: string;
  /** The citation, for the button's accessible name ("42 U.S.C. 1395w-4"). */
  citation: string;
}

type State =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "loaded"; section: LawSectionResponse }
  | { status: "error" };

/**
 * A disclosure showing a US Code section's current text, fetched from GET /law/{title}/{section}
 * the first time it's opened, so the bill page itself stays one cached render (#316).
 */
export function LawSectionText({ title, section, citation }: LawSectionTextProps) {
  const [expanded, setExpanded] = useState(false);
  const [state, setState] = useState<State>({ status: "idle" });
  const regionId = useId();

  async function load() {
    setState({ status: "loading" });
    try {
      setState({ status: "loaded", section: await getLawSection(title, section) });
    } catch (err) {
      reportError(err);
      setState({ status: "error" });
    }
  }

  function toggle() {
    const next = !expanded;
    setExpanded(next);
    if (next && (state.status === "idle" || state.status === "error")) void load();
  }

  return (
    <div>
      <Button
        variant="ghost"
        size="sm"
        onClick={toggle}
        aria-expanded={expanded}
        aria-controls={regionId}
        aria-label={`${expanded ? "Hide" : "Show"} current text of ${citation}`}
      >
        {expanded ? "Hide current text" : "Show current text"}
      </Button>
      <div id={regionId} hidden={!expanded} className="mt-2">
        {state.status === "loading" && (
          <p role="status" className="text-sm text-muted-foreground">
            Loading the current text…
          </p>
        )}
        {state.status === "error" && (
          <p role="alert" className="text-sm text-muted-foreground">
            The current text couldn&apos;t be loaded. Try again, or read it on uscode.house.gov.
          </p>
        )}
        {state.status === "loaded" && (
          <div
            tabIndex={0}
            role="region"
            aria-label={`Current text of ${citation}`}
            className="max-h-96 overflow-y-auto rounded-md border border-border bg-muted/50 p-3"
          >
            <p className="text-sm text-foreground leading-relaxed whitespace-pre-wrap">{state.section.text}</p>
          </div>
        )}
      </div>
    </div>
  );
}
