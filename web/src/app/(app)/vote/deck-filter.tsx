"use client";

import { type ReactNode, useId, useRef, useState } from "react";
import { BillFilterFields } from "@/components/bill/bill-filter-fields";
import { Input } from "@/components/ui/input";
import { ALL_CONGRESSES } from "@/lib/congress";
import { billFiltersQuery, type BillFilters } from "@/lib/bill-filters";
import { clampSearch } from "@/lib/paging";
import { BILL_SORTS, BILL_VIEWS, parseBillView } from "@/lib/bill-views";
import { BILL_TYPE_LABELS, type Congress } from "@/lib/types";

interface DeckFilterProps {
  filters: BillFilters;
  congresses: Congress[];
  /** The congress in session, which `congress: undefined` stands for. */
  current: number | undefined;
  /** Every policy area, A to Z; null when they couldn't be read, which leaves the select out. */
  policyAreas: readonly string[] | null;
  onChange: (change: Partial<BillFilters>) => void;
  /** Clears every filter: the default deck. */
  onClear: () => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** What shares the bar with the Filter button, before it: the page's title. */
  lead?: ReactNode;
}

/** The filters in words, for the Filter button's name: the view, then whatever else is set. */
export function describeFilters(f: BillFilters, current: number | undefined): string {
  const parts = [parseBillView(f.view).label];
  if (f.q) parts.push(`search “${f.q}”`);
  if (f.sort !== "latest_action") parts.push(`by ${BILL_SORTS.find((s) => s.value === f.sort)?.label.toLowerCase()}`);
  if (f.area) parts.push(f.area);
  if (f.congress === ALL_CONGRESSES) parts.push("all congresses");
  else if (f.congress !== undefined && f.congress !== current) parts.push(`${f.congress}th Congress`);
  if (f.type) parts.push(BILL_TYPE_LABELS[f.type]);
  if (f.chamber) parts.push(f.chamber === "house" ? "started in the House" : "started in the Senate");
  return parts.join(", ");
}

/**
 * /vote's filter (#663, #797): a single Filter button, closed by default, so the deck stays near
 * the top. The button ends the page's one bar (#717), after `lead`; a dot on it says a filter
 * other than the default is on. Open, it holds what /bills offers: the four views, a search, and
 * the panel fields /bills shows (sort, policy area, congress, bill type, chamber of origin).
 */
export function DeckFilter({
  filters,
  congresses,
  current,
  policyAreas,
  onChange,
  onClear,
  open,
  onOpenChange,
  lead,
}: DeckFilterProps) {
  const panelId = useId();
  const searchId = useId();
  const searchTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const changed = billFiltersQuery(filters) !== "";
  // What's in the search box. A new URL (Back, Clear filters) puts its search back in the box,
  // unless the box already says it, so the visitor's own search never resets what they're typing.
  const [text, setText] = useState(filters.q ?? "");
  const [urlQ, setUrlQ] = useState(filters.q);
  if (filters.q !== urlQ) {
    setUrlQ(filters.q);
    if (clampSearch(text) !== filters.q) setText(filters.q ?? "");
  }

  return (
    <div>
      <div className="flex items-center justify-end gap-2 border-b border-border pb-2 sm:gap-3 sm:pb-3">
        {lead}
        <button
          type="button"
          onClick={() => onOpenChange(!open)}
          aria-expanded={open}
          aria-controls={open ? panelId : undefined}
          aria-label={`Filter: ${describeFilters(filters, current)}`}
          className="relative inline-flex min-h-11 shrink-0 items-center gap-1.5 rounded-lg border border-border bg-card px-3 text-sm font-medium text-foreground hover:bg-muted/50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
        >
          <FilterIcon className="h-4 w-4" />
          Filter
          {changed && <span className="h-2 w-2 rounded-full bg-foreground" aria-hidden="true" />}
        </button>
      </div>
      {open && (
        <div id={panelId} className="mt-2 space-y-4 rounded-xl border border-border bg-card px-3 py-3">
          <fieldset>
            <legend className="mb-1.5 text-sm font-medium text-foreground">Show</legend>
            <div className="grid grid-cols-2 gap-1 rounded-lg bg-muted p-1 sm:grid-cols-4">
              {BILL_VIEWS.map((v) => (
                <ViewOption
                  key={v.key}
                  label={v.label}
                  checked={filters.view === v.key}
                  onPick={() => onChange({ view: v.key })}
                />
              ))}
            </div>
          </fieldset>
          <div className="space-y-1.5">
            <label htmlFor={searchId} className="block text-sm font-medium text-foreground">
              Search bills
            </label>
            <Input
              id={searchId}
              type="search"
              className="h-10"
              value={text}
              onChange={(e) => {
                const value = e.target.value;
                setText(value);
                clearTimeout(searchTimer.current);
                searchTimer.current = setTimeout(() => onChange({ q: value || undefined }), 300);
              }}
            />
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <BillFilterFields
              filters={filters}
              congresses={congresses}
              policyAreas={policyAreas}
              current={current}
              onChange={onChange}
            />
          </div>
          <div className="flex justify-end gap-2">
            {changed && (
              <button
                type="button"
                onClick={onClear}
                className="min-h-11 rounded-lg px-3 text-sm font-medium text-foreground hover:bg-muted/50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
              >
                Clear filters
              </button>
            )}
            <button
              type="button"
              onClick={() => onOpenChange(false)}
              className="min-h-11 rounded-lg px-3 text-sm font-medium text-foreground hover:bg-muted/50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            >
              Done
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

/** One segment: a native radio, visually hidden, so the arrow keys and screen readers work as usual. */
function ViewOption({ label, checked, onPick }: { label: string; checked: boolean; onPick: () => void }) {
  return (
    <label
      className={`flex min-h-11 cursor-pointer items-center justify-center rounded-md px-1 text-center text-xs font-medium leading-tight has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-ring ${
        checked ? "bg-card font-semibold text-foreground shadow-sm ring-1 ring-border" : "text-muted-foreground hover:text-foreground"
      }`}
    >
      <input type="radio" name="deck-view" checked={checked} onChange={onPick} className="sr-only" />
      {label}
    </label>
  );
}

function FilterIcon({ className }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2} aria-hidden="true">
      <path strokeLinecap="round" strokeLinejoin="round" d="M10.5 6h9.75M10.5 6a1.5 1.5 0 11-3 0m3 0a1.5 1.5 0 10-3 0M3.75 6H7.5m3 12h9.75m-9.75 0a1.5 1.5 0 01-3 0m3 0a1.5 1.5 0 00-3 0m-3.75 0H7.5m9-6h3.75m-3.75 0a1.5 1.5 0 01-3 0m3 0a1.5 1.5 0 00-3 0m-9.75 0h9.75" />
    </svg>
  );
}
