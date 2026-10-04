"use client";

import { useId } from "react";
import { Select } from "@/components/ui/select";
import type { BillChamber, BillFilters } from "@/lib/bill-filters";
import { BILL_SORTS, type BillSort } from "@/lib/bill-views";
import { ALL_CONGRESSES } from "@/lib/congress";
import type { BillType, Congress } from "@/lib/types";
import { BILL_TYPE_LABELS } from "@/lib/types";

// The fields of the filter panel /bills and /vote share (#785): sort, policy area, congress, bill
// type and chamber of origin. Each page lays out its own bar and panel around them.

/** A change to the filters, plus "Unvoted only", which only /bills shows. */
export type BillFilterChange = Partial<BillFilters> & { unvoted?: boolean };

export interface BillFilterFieldsProps {
  filters: BillFilters;
  congresses: Congress[];
  /**
   * Every policy area, A to Z; null when they couldn't be read, which leaves the select out (as
   * does an empty list, unless the URL names an area).
   */
  policyAreas: readonly string[] | null;
  /** The congress in session, which `congress: undefined` stands for. */
  current: number | undefined;
  onChange: (change: BillFilterChange) => void;
  /** Show "Only bills I haven't voted on" (signed-in /bills only). */
  showUnvoted?: boolean;
  unvoted?: boolean;
}

/** The select's value for the filters' congress: the current one when the URL names none. */
function congressValue(congress: BillFilters["congress"], current: number | undefined): string {
  if (congress !== undefined) return String(congress);
  return current === undefined ? ALL_CONGRESSES : String(current);
}

/** The filters' congress for a choice in the select: undefined for the current one. */
function congressChoice(value: string, current: number | undefined): BillFilters["congress"] {
  if (value === ALL_CONGRESSES) return current === undefined ? undefined : ALL_CONGRESSES;
  const n = Number(value);
  return n === current ? undefined : n;
}

export function BillFilterFields({
  filters,
  congresses,
  policyAreas,
  current,
  onChange,
  showUnvoted = false,
  unvoted = false,
}: BillFilterFieldsProps) {
  const sortId = useId();
  const congressId = useId();
  // A URL can name an area the list no longer has (or never had): keep it selectable, so the
  // select shows what the list is filtered by.
  const areas =
    policyAreas && filters.area && !policyAreas.includes(filters.area) ? [filters.area, ...policyAreas] : policyAreas;

  return (
    <>
      <div className="space-y-1.5">
        <label htmlFor={sortId} className="block text-sm font-medium text-foreground">
          Sort by
        </label>
        <Select id={sortId} value={filters.sort} onChange={(e) => onChange({ sort: e.target.value as BillSort })}>
          {BILL_SORTS.map((s) => (
            <option key={s.value} value={s.value}>
              {s.label}
            </option>
          ))}
        </Select>
      </div>
      {areas && areas.length > 0 && (
        <FilterSelect
          label="Policy area"
          value={filters.area ?? ""}
          onChange={(v) => onChange({ area: v || undefined })}
          options={areas.map((a) => ({ value: a, label: a }))}
        />
      )}
      <div className="space-y-1.5">
        <label htmlFor={congressId} className="block text-sm font-medium text-foreground">
          Congress
        </label>
        <Select
          id={congressId}
          value={congressValue(filters.congress, current)}
          onChange={(e) => onChange({ congress: congressChoice(e.target.value, current) })}
        >
          {congresses.map((c) => (
            <option key={c.number} value={c.number}>
              {c.number}th{c.number === current ? " (current)" : ""}
            </option>
          ))}
          <option value={ALL_CONGRESSES}>All congresses</option>
        </Select>
      </div>
      <FilterSelect
        label="Bill type"
        value={filters.type ?? ""}
        onChange={(v) => onChange({ type: (v || undefined) as BillType | undefined })}
        options={Object.entries(BILL_TYPE_LABELS).map(([value, label]) => ({ value, label }))}
      />
      <FilterSelect
        label="Chamber of origin"
        value={filters.chamber ?? ""}
        onChange={(v) => onChange({ chamber: (v || undefined) as BillChamber | undefined })}
        options={[
          { value: "house", label: "House" },
          { value: "senate", label: "Senate" },
        ]}
      />
      {showUnvoted && (
        <label className="flex cursor-pointer items-center gap-2 sm:col-span-2 lg:col-span-4">
          <input
            type="checkbox"
            checked={unvoted}
            onChange={(e) => onChange({ unvoted: e.target.checked })}
            className="h-4 w-4 rounded border-border accent-primary"
          />
          <span className="text-sm text-muted-foreground">Show only bills I haven&apos;t voted on</span>
        </label>
      )}
    </>
  );
}

function FilterSelect({
  label,
  value,
  onChange,
  options,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  options: { value: string; label: string }[];
}) {
  const id = useId();
  return (
    <div className="space-y-1.5">
      <label htmlFor={id} className="block text-sm font-medium text-foreground">
        {label}
      </label>
      <Select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">All</option>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </Select>
    </div>
  );
}
