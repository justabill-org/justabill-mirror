"use client";

import { useState, useEffect, useId } from "react";
import type { BillText, BillTextSection } from "@/lib/types";
import { getBillText } from "@/lib/api";
import { isNotFound } from "@/lib/fallback";
import { contentsLabel, listsChildrenInContents, sectionHeading } from "@/lib/bill-text";
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
  const [activeSection, setActiveSection] = useState<string | null>(null);
  const contentsId = useId();

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
    return (
      <div className="flex h-full max-h-[80vh]">
        {/* Table of Contents - Desktop sidebar */}
        <aside className="hidden lg:block w-64 flex-shrink-0 border-r border-border overflow-y-auto p-4">
          <h3 id={contentsId} className="text-sm font-semibold text-foreground mb-3">Contents</h3>
          <nav aria-labelledby={contentsId} className="space-y-1">
            {billText.sections.map((section) => (
              <TableOfContentsItem
                key={section.id}
                section={section}
                activeSection={activeSection}
                onNavigate={setActiveSection}
              />
            ))}
          </nav>
        </aside>

        {/* The text. Not a <main>: the reader sits inside the page's own main landmark. */}
        <div className="flex-1 overflow-y-auto p-6">
          <div className="max-w-3xl mx-auto space-y-6">
            {billText.sections.map((section) => (
              <SectionDisplay
                key={section.id}
                section={section}
                activeSection={activeSection}
              />
            ))}
          </div>
        </div>
      </div>
    );
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

  const handleClick = () => {
    onNavigate(section.id);
    // Scroll to section
    const element = document.getElementById(`section-${section.id}`);
    if (element) {
      element.scrollIntoView({ behavior: "smooth", block: "start" });
    }
  };

  return (
    <div>
      <button
        onClick={handleClick}
        className={cn(
          "w-full text-left text-sm py-1.5 px-2 rounded-md transition-colors hover:bg-muted",
          isActive && "bg-muted text-foreground font-medium",
          !isActive && "text-muted-foreground",
          depth > 0 && "ml-3"
        )}
        style={{ paddingLeft: `${(depth * 12) + 8}px` }}
      >
        <span className="line-clamp-1">{contentsLabel(section)}</span>
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
      className={cn(
        "scroll-mt-6 rounded-lg p-4 -mx-4 transition-colors",
        isActive && "bg-muted/40"
      )}
    >
      {heading && (
        <HeadingTag
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
