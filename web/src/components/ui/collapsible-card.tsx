"use client";

import { useId, useState, useSyncExternalStore, type ReactNode } from "react";
import { Card } from "@/components/ui/card";

/** The lg breakpoint: from here the bill page has room to show every card open. */
const WIDE_QUERY = "(min-width: 1024px)";

// Without matchMedia (jsdom, very old browsers) the card acts as on a narrow screen.
function subscribeWide(onChange: () => void) {
  if (typeof window.matchMedia !== "function") return () => {};
  const query = window.matchMedia(WIDE_QUERY);
  query.addEventListener("change", onChange);
  return () => query.removeEventListener("change", onChange);
}

function isWide() {
  return typeof window.matchMedia === "function" && window.matchMedia(WIDE_QUERY).matches;
}

interface CollapsibleCardProps {
  title: ReactNode;
  /** One line shown under the title while the card is closed, e.g. the current step or a count. */
  summary?: ReactNode;
  /** Open on phones and tablets before the visitor toggles it. Wide screens always start open. */
  defaultOpenNarrow?: boolean;
  titleClassName?: string;
  className?: string;
  contentClassName?: string;
  children: ReactNode;
}

/**
 * A card whose body folds away behind its title (#664), so a phone shows the page's sections as a
 * short list. It starts closed below the lg breakpoint (unless defaultOpenNarrow) and open from
 * lg. The body stays in the HTML and is hidden with CSS only, so the static render is the same at
 * every width and wide screens never flash closed before hydration.
 */
export function CollapsibleCard({
  title,
  summary,
  defaultOpenNarrow = false,
  titleClassName = "text-lg",
  className = "",
  contentClassName = "",
  children,
}: CollapsibleCardProps) {
  const contentId = useId();
  const wide = useSyncExternalStore(subscribeWide, isWide, () => false);
  const [toggled, setToggled] = useState<boolean | null>(null);
  const open = toggled ?? (wide || defaultOpenNarrow);
  // Untouched, the width decides in CSS; once toggled, the visitor's choice holds at every width.
  const hidden = toggled === null ? (defaultOpenNarrow ? "" : "max-lg:hidden") : open ? "" : "hidden";

  return (
    <Card className={className}>
      <h3 className={`font-semibold leading-none tracking-tight ${titleClassName}`}>
        <button
          type="button"
          aria-expanded={open}
          aria-controls={contentId}
          onClick={() => setToggled(!open)}
          className="flex w-full items-start justify-between gap-3 rounded-xl p-4 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:p-6"
        >
          <span className="min-w-0">
            <span className="block leading-6">{title}</span>
            {summary && !open && (
              <span
                className={`mt-1 line-clamp-2 text-sm font-normal leading-snug tracking-normal text-muted-foreground ${
                  toggled === null ? "lg:hidden" : ""
                }`}
              >
                {summary}
              </span>
            )}
          </span>
          <svg
            aria-hidden="true"
            viewBox="0 0 20 20"
            fill="currentColor"
            className={`mt-0.5 h-5 w-5 shrink-0 text-muted-foreground transition-transform motion-reduce:transition-none ${
              open ? "rotate-180" : ""
            }`}
          >
            <path
              fillRule="evenodd"
              d="M5.23 7.21a.75.75 0 011.06.02L10 11.17l3.71-3.94a.75.75 0 111.08 1.04l-4.25 4.5a.75.75 0 01-1.08 0l-4.25-4.5a.75.75 0 01.02-1.06z"
              clipRule="evenodd"
            />
          </svg>
        </button>
      </h3>
      <div id={contentId} className={`px-4 pb-4 sm:px-6 sm:pb-6 ${hidden} ${contentClassName}`}>
        {children}
      </div>
    </Card>
  );
}
