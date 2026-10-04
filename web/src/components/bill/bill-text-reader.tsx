"use client";

import { useState, useEffect, useId, useRef } from "react";
import { flushSync } from "react-dom";
import type { BillText, BillTextSection } from "@/lib/types";
import { getBillText } from "@/lib/api";
import { isNotFound } from "@/lib/fallback";
import { contentsLabel, listsChildrenInContents, sectionHeading } from "@/lib/bill-text";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

interface BillTextReaderProps {
  billId: string;
  versionId: string;
}

export function BillTextReader({ billId, versionId }: BillTextReaderProps) {
  const [billText, setBillText] = useState<BillText | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    getBillText(billId, versionId)
      .then((response) => {
        if (!cancelled) setBillText(response);
      })
      .catch((err: unknown) => {
        // A 404 is a version whose text the pipeline hasn't fetched yet: there's no text, nothing failed.
        if (!cancelled && !isNotFound(err)) setError("Failed to load bill text.");
      })
      .finally(() => {
        if (!cancelled) setIsLoading(false);
      });
    return () => { cancelled = true; };
  }, [billId, versionId]);

  if (isLoading) {
    return (
      <div className="space-y-4 p-6">
        <Skeleton className="h-8 w-1/3" />
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-3/4" />
        <div className="pt-4" />
        <Skeleton className="h-6 w-1/4" />
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-2/3" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="p-6">
        <p className="text-sm text-destructive">{error}</p>
      </div>
    );
  }

  if (!billText || (!billText.sections?.length && !billText.content?.trim())) {
    return (
      <div className="p-6">
        <p className="text-sm text-muted-foreground">No text content available.</p>
      </div>
    );
  }

  // If we have parsed sections, display structured view
  if (billText.sections && billText.sections.length > 0) {
    return <SectionedText sections={billText.sections} />;
  }

  // Fallback: raw content display
  return (
    <div className="p-6 overflow-y-auto max-h-[80vh]">
      <pre className="whitespace-pre-wrap font-mono text-sm text-foreground leading-relaxed">
        {billText.content}
      </pre>
    </div>
  );
}

/** The width from which the contents is a sidebar beside the text (Tailwind's `lg`). */
const SIDEBAR_QUERY = "(min-width: 1024px)";

/** The browser's media query, or null where there's none (jsdom). */
function mediaQuery(query: string): MediaQueryList | null {
  return typeof window.matchMedia === "function" ? window.matchMedia(query) : null;
}

/**
 * The parsed text with its contents (#883). From 1024 px the contents is a sidebar beside the
 * text. Below it there's no room for one, so a Contents button above the text opens the same list
 * over the text; choosing an entry closes it and moves focus to that section's heading.
 */
function SectionedText({ sections }: { sections: BillTextSection[] }) {
  const [activeSection, setActiveSection] = useState<string | null>(null);
  // Whether the contents is open over the text (below 1024 px only).
  const [listOpen, setListOpen] = useState(false);
  const contentsId = useId();
  const listId = useId();
  const buttonRef = useRef<HTMLButtonElement>(null);
  const rootRef = useRef<HTMLDivElement>(null);

  // Widening past 1024 px (a phone turned sideways) turns the open list into the sidebar, so it
  // must stop covering the text, or the text would stay inert.
  useEffect(() => {
    const sidebar = mediaQuery(SIDEBAR_QUERY);
    if (!sidebar) return;
    const onChange = () => {
      if (sidebar.matches) setListOpen(false);
    };
    sidebar.addEventListener("change", onChange);
    return () => sidebar.removeEventListener("change", onChange);
  }, []);

  const closeList = () => {
    setListOpen(false);
    buttonRef.current?.focus();
  };

  // Escape closes the open list and nothing else: the sheet around the reader closes on any
  // Escape that reaches document. A native listener on the reader, not React's onKeyDown: Next
  // hydrates React onto document itself, where stopping propagation can't stop the sheet's listener.
  useEffect(() => {
    const root = rootRef.current;
    if (!listOpen || !root) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      setListOpen(false);
      buttonRef.current?.focus();
    };
    root.addEventListener("keydown", onKeyDown);
    return () => root.removeEventListener("keydown", onKeyDown);
  }, [listOpen]);

  const navigate = (id: string) => {
    const fromList = listOpen;
    // The text is inert while the list covers it: take the list away before focusing into it.
    flushSync(() => {
      setActiveSection(id);
      setListOpen(false);
    });
    const element = document.getElementById(`section-${id}`);
    if (!element) return;
    const reduceMotion = mediaQuery("(prefers-reduced-motion: reduce)")?.matches ?? false;
    element.scrollIntoView({ behavior: reduceMotion ? "auto" : "smooth", block: "start" });
    if (fromList) {
      const target = element.querySelector<HTMLElement>(":scope > [data-section-heading]") ?? element;
      target.focus({ preventScroll: true });
    }
  };

  return (
    <div ref={rootRef} className="flex h-full max-h-[80vh] flex-col lg:flex-row">
      {/* Contents button, below 1024 px: outside the scrolling text, so it's always in reach. */}
      <div className="flex-shrink-0 border-b border-border px-4 py-2 lg:hidden">
        <Button
          ref={buttonRef}
          type="button"
          variant="outline"
          aria-expanded={listOpen}
          aria-controls={listId}
          onClick={() => (listOpen ? closeList() : setListOpen(true))}
        >
          Contents
        </Button>
      </div>

      <div className="relative flex min-h-0 flex-1">
        {/* The contents: a sidebar from 1024 px, and below it a list over the text while open. */}
        <aside
          id={listId}
          className={cn(
            "overflow-y-auto overscroll-contain p-4",
            "lg:static lg:block lg:w-64 lg:flex-shrink-0 lg:border-r lg:border-border",
            listOpen ? "absolute inset-0 z-10 bg-background" : "hidden"
          )}
        >
          <h3 id={contentsId} className="text-sm font-semibold text-foreground mb-3">Contents</h3>
          <nav aria-labelledby={contentsId} className="space-y-1">
            {sections.map((section) => (
              <TableOfContentsItem
                key={section.id}
                section={section}
                activeSection={activeSection}
                onNavigate={navigate}
              />
            ))}
          </nav>
        </aside>

        {/* The text. Not a <main>: the reader sits inside the page's own main landmark. Inert
            while the list covers it, so Tab and screen readers can't reach text behind the list. */}
        <div className="flex-1 overflow-y-auto p-6" inert={listOpen}>
          <div className="max-w-3xl mx-auto space-y-6">
            {sections.map((section) => (
              <SectionDisplay
                key={section.id}
                section={section}
                activeSection={activeSection}
              />
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

interface TableOfContentsItemProps {
  section: BillTextSection;
  activeSection: string | null;
  onNavigate: (id: string) => void;
  depth?: number;
}

function TableOfContentsItem({
  section,
  activeSection,
  onNavigate,
  depth = 0,
}: TableOfContentsItemProps) {
  const isActive = activeSection === section.id;

  return (
    <div>
      <button
        type="button"
        onClick={() => onNavigate(section.id)}
        className={cn(
          // Two lines and 44 px tall on a phone, so long headings stay tellable and easy to tap.
          "w-full min-h-11 text-left text-sm py-1.5 px-2 rounded-md transition-colors hover:bg-muted lg:min-h-0",
          isActive && "bg-muted text-foreground font-medium",
          !isActive && "text-muted-foreground",
          depth > 0 && "ml-3"
        )}
        style={{ paddingLeft: `${(depth * 12) + 8}px` }}
      >
        <span className="line-clamp-2 [overflow-wrap:anywhere] lg:line-clamp-1">{contentsLabel(section)}</span>
      </button>
      {listsChildrenInContents(section) && section.children?.map((child) => (
        <TableOfContentsItem
          key={child.id}
          section={child}
          activeSection={activeSection}
          onNavigate={onNavigate}
          depth={depth + 1}
        />
      ))}
    </div>
  );
}

interface SectionDisplayProps {
  section: BillTextSection;
  activeSection: string | null;
  depth?: number;
}

function SectionDisplay({ section, activeSection, depth = 0 }: SectionDisplayProps) {
  const isActive = activeSection === section.id;
  const HeadingTag = depth === 0 ? "h3" : depth === 1 ? "h4" : "h5";
  const heading = sectionHeading(section);

  return (
    <section
      id={`section-${section.id}`}
      // A section with no heading takes the focus itself.
      tabIndex={heading ? undefined : -1}
      className={cn(
        "scroll-mt-6 rounded-lg p-4 -mx-4 transition-colors",
        isActive && "bg-muted/40"
      )}
    >
      {heading && (
        <HeadingTag
          // Focus lands here when the section is chosen from the contents on a phone.
          tabIndex={-1}
          data-section-heading=""
          className={cn(
            "font-semibold text-foreground mb-2",
            depth === 0 && "text-lg",
            depth === 1 && "text-base",
            depth >= 2 && "text-sm"
          )}
        >
          {heading}
        </HeadingTag>
      )}

      {section.content && (
        <p className="text-sm text-foreground leading-relaxed whitespace-pre-wrap">
          {section.content}
        </p>
      )}

      {/* Nested children */}
      {section.children && section.children.length > 0 && (
        <div className="mt-4 pl-4 border-l-2 border-border space-y-4">
          {section.children.map((child) => (
            <SectionDisplay
              key={child.id}
              section={child}
              activeSection={activeSection}
              depth={depth + 1}
            />
          ))}
        </div>
      )}
    </section>
  );
}
