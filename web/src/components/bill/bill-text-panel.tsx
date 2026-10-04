"use client";

import { useId, useState, type ReactNode } from "react";
import Link from "next/link";
import type { BillAction, BillLaw, BillStatus, BillTextDiff, BillTextVersion, GraphRelatedBill } from "@/lib/types";
import { BILL_STATUS_LABELS } from "@/lib/types";
import {
  billChamber, companionBills, leadText, officialName, otherChamber, stageLabel, versionSteps, type VersionStep,
} from "@/lib/text-versions";
import { billLabel } from "@/lib/graph";
import { formatDate } from "@/lib/utils";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetClose, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { BillTextReader } from "@/components/bill/bill-text-reader";
import { DiffCard } from "@/components/diff/diff-viewer";

interface BillTextPanelProps {
  billId: string;
  billType: string;
  currentStatus?: BillStatus;
  versions: BillTextVersion[];
  /** The stored diffs between versions; null when that section of the bill failed to load. */
  diffs: BillTextDiff[] | null;
  actions: BillAction[] | null;
  /** The laws the bill became (bill.laws); the number is read from the actions only without them. */
  laws?: BillLaw[];
  related: GraphRelatedBill[];
}

/**
 * The bill page's Text tab (#665): the final text first (the Public Law print, else the enrolled
 * bill), or the latest version marked as not final; then every version oldest first with the
 * changes between neighbors; then the other chamber's companion bills. Each section folds to one
 * line: the final text starts open, the versions and companions start folded.
 */
export function BillTextPanel(props: BillTextPanelProps) {
  const { billId, billType, versions, diffs, actions, laws, related } = props;
  const [readerVersion, setReaderVersion] = useState<BillTextVersion | null>(null);
  const lead = leadText(versions, billType, actions, laws);
  const steps = versionSteps(versions, diffs);
  const companions = companionBills(related, billType);

  return (
    <div className="space-y-3">
      {lead ? (
        <LeadCard lead={lead} step={steps.find((s) => s.version.id === lead.version.id)} status={props.currentStatus}
          onRead={setReaderVersion} />
      ) : (
        <Card>
          <CardHeader className="p-4 sm:px-5">
            <CardTitle className="text-base">Text</CardTitle>
          </CardHeader>
          <CardContent className="px-4 pb-4 sm:px-5">
            <p className="text-sm text-muted-foreground">No text versions available yet.</p>
          </CardContent>
        </Card>
      )}

      {steps.length > 1 && (
        <VersionsCard steps={steps} billId={billId} finalId={lead?.isFinal ? lead.version.id : undefined}
          diffsFailed={diffs === null} onRead={setReaderVersion} />
      )}

      {companions.length > 0 && <CompanionsCard companions={companions} chamber={otherChamber(billChamber(billType))} />}

      <Sheet open={readerVersion !== null} onOpenChange={() => setReaderVersion(null)}>
        <SheetContent side="bottom" className="h-[90vh] flex flex-col">
          <SheetHeader className="flex-shrink-0">
            <SheetTitle>{readerVersion ? officialName(readerVersion) : ""}</SheetTitle>
            <SheetClose onClick={() => setReaderVersion(null)} />
          </SheetHeader>
          {readerVersion && (
            <div className="flex-1 overflow-hidden -mx-6 -mb-6">
              <BillTextReader billId={billId} versionId={readerVersion.id} />
            </div>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}

interface LeadCardProps {
  lead: NonNullable<ReturnType<typeof leadText>>;
  step?: VersionStep;
  status?: BillStatus;
  onRead: (version: BillTextVersion) => void;
}

function LeadCard({ lead, step, status, onRead }: LeadCardProps) {
  const { version } = lead;
  const isLaw = ["pl", "public_law"].includes(version.version_code.toLowerCase());
  const date = version.date ? formatDate(version.date, "long") : null;
  const detail = leadDetail(lead, isLaw, date, status);

  return (
    <CollapsibleCard
      title={lead.isFinal ? "Final text" : "Latest version"}
      badge={lead.isFinal ? (
        <Badge variant="success">{lead.law ?? step?.officialName}</Badge>
      ) : (
        <Badge variant="secondary">Not final</Badge>
      )}
      summary={step?.officialName ?? officialName(version)}
      defaultOpen
      className={lead.isFinal ? "border-success/40" : ""}
    >
      <div className="space-y-3">
        <div>
          <p className="text-sm font-medium text-foreground">{step?.officialName ?? officialName(version)}</p>
          {detail && <p className="text-sm text-muted-foreground">{detail}</p>}
        </div>
        <p className="text-sm text-muted-foreground">{leadExplanation(lead, isLaw)}</p>
        <VersionActions version={version} label={lead.isFinal ? "Read the final text" : "Read this version"} onRead={onRead} />
      </div>
    </CollapsibleCard>
  );
}

function leadDetail(lead: LeadCardProps["lead"], isLaw: boolean, date: string | null, status?: BillStatus): string {
  const parts: string[] = [];
  if (lead.law) parts.push(lead.law);
  if (lead.enactedDate) parts.push(`became law ${formatDate(lead.enactedDate, "long")}`);
  else if (date) parts.push(isLaw ? `printed ${date}` : date);
  if (!lead.isFinal && status) parts.push(`Status: ${BILL_STATUS_LABELS[status] ?? status}`);
  return parts.join(" · ");
}

function leadExplanation(lead: LeadCardProps["lead"], isLaw: boolean): string {
  if (isLaw) return "The text of the law as enacted.";
  if (lead.isFinal) {
    return lead.version.version_code.toLowerCase() === "enr"
      ? "The text both chambers passed in the same words and sent to the President."
      : "The text as agreed to.";
  }
  return "Not the final text yet: a bill's text is final once both chambers pass it in the same words, so it may still change.";
}

/** Short visible names for GovInfo's formats; the link's accessible name keeps the full one. */
const FORMAT_LABELS: Record<string, string> = {
  "formatted text": "Text",
  pdf: "PDF",
  "formatted xml": "XML",
  "united states legislative markup": "USLM",
};

interface VersionActionsProps {
  version: BillTextVersion;
  label: string;
  onRead: (version: BillTextVersion) => void;
}

function VersionActions({ version, label, onRead }: VersionActionsProps) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button variant="outline" size="sm" onClick={() => onRead(version)} className="text-xs">
        {label}
      </Button>
      {version.formats.map((format) => {
        const short = FORMAT_LABELS[format.type.toLowerCase()] ?? format.type.toUpperCase();
        return (
        <a
          key={format.type}
          href={format.url}
          target="_blank"
          rel="noopener noreferrer"
          aria-label={`${short}: ${officialName(version)}, ${format.type} on GovInfo (opens in a new tab)`}
          className="inline-flex min-h-8 items-center rounded-md border border-border px-3 text-xs font-medium text-muted-foreground hover:bg-muted hover:text-foreground transition-colors"
        >
          {short}
        </a>
        );
      })}
    </div>
  );
}

interface VersionsCardProps {
  steps: VersionStep[];
  billId: string;
  finalId?: string;
  /** The diffs failed to load, so a pair without one may still have a comparison. */
  diffsFailed: boolean;
  onRead: (version: BillTextVersion) => void;
}

function VersionsCard({ steps, billId, finalId, diffsFailed, onRead }: VersionsCardProps) {
  const [expanded, setExpanded] = useState<string | null>(null);
  const first = steps[0];
  const last = steps[steps.length - 1];

  return (
    <CollapsibleCard
      title="Versions"
      badge={<Badge variant="outline">{steps.length}</Badge>}
      summary={`${first.step} → ${last.step}${last.version.date ? `, ${formatDate(last.version.date, "medium")}` : ""}`}
    >
      <p className="mb-3 text-xs text-muted-foreground">Oldest first, with the changes from one version to the next.</p>
      <ol aria-label="Versions" className="space-y-2">
        {steps.map((s) => {
          const change = s.changesFromPrevious;
          return (
          <li key={s.version.id} className="space-y-2">
            {s.previous && (
              change ? (
                <div className="ml-3 border-l-2 border-border pl-2 sm:ml-6 sm:pl-3">
                  <DiffCard
                    diff={change}
                    billId={billId}
                    title={`Changes: ${officialName(s.previous)} → ${s.officialName}`}
                    isExpanded={expanded === change.id}
                    onToggle={() => setExpanded(expanded === change.id ? null : change.id)}
                    compact
                  />
                </div>
              ) : (
                <p className="ml-3 border-l-2 border-border py-1 pl-2 text-xs text-muted-foreground sm:ml-6 sm:pl-3">
                  {diffsFailed
                    ? "The changes from the previous version couldn't be loaded right now."
                    : "No comparison with the previous version yet."}
                </p>
              )
            )}
            <div className={`flex flex-col gap-2 rounded-lg border px-3 py-2 sm:flex-row sm:items-center sm:justify-between ${
              s.version.id === finalId ? "border-success/40 bg-success/5" : "border-border"}`}>
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  {stageLabel(s.version) && <Badge variant="outline">{stageLabel(s.version)}</Badge>}
                  <p className="text-sm font-medium text-foreground">{s.step}</p>
                  {s.version.id === finalId && <Badge variant="success">Final text</Badge>}
                  <p className="text-xs text-muted-foreground">
                    {[s.step !== s.officialName ? s.officialName : "", s.version.date ? formatDate(s.version.date, "long") : ""]
                      .filter(Boolean).join(" · ")}
                  </p>
                </div>
              </div>
              <VersionActions version={s.version} label="Read" onRead={onRead} />
            </div>
          </li>
          );
        })}
      </ol>
    </CollapsibleCard>
  );
}

function CompanionsCard({ companions, chamber }: { companions: GraphRelatedBill[]; chamber: string }) {
  return (
    <CollapsibleCard
      title="Companion bills"
      badge={<Badge variant="outline">{companions.length}</Badge>}
      summary={companions.map((c) => billLabel(c.bill_type, c.number)).join(", ")}
    >
      <p className="mb-3 text-xs text-muted-foreground">
        {chamber} bills Congress.gov lists as identical or related to this one. Each has its own number, text and votes.
      </p>
      <ul className="space-y-2">
        {companions.map((c) => (
          <li key={c.bill_id}>
            <Link
              href={`/bills/${c.bill_id}`}
              className="flex flex-col gap-1 rounded-lg border border-border px-3 py-2 hover:bg-muted transition-colors sm:flex-row sm:items-start sm:justify-between sm:gap-3"
            >
              <div className="min-w-0">
                <p className="text-sm">
                  <span className="text-muted-foreground">{chamber} companion: </span>
                  <span className="font-semibold tabular-nums text-foreground">{billLabel(c.bill_type, c.number)}</span>
                </p>
                <p className="mt-0.5 text-sm text-muted-foreground">{c.title}</p>
              </div>
              <div className="flex shrink-0 flex-wrap items-center gap-2 sm:flex-col sm:items-end sm:gap-1">
                {c.relation_types.filter((t) => t === "Identical bill" || t === "Related bill").map((t) => (
                  <Badge key={t} variant="outline">{t}</Badge>
                ))}
                {c.current_status && (
                  <span className="text-xs text-muted-foreground">
                    Status: {BILL_STATUS_LABELS[c.current_status] ?? c.current_status}
                  </span>
                )}
              </div>
            </Link>
          </li>
        ))}
      </ul>
    </CollapsibleCard>
  );
}

interface CollapsibleCardProps {
  title: string;
  badge?: ReactNode;
  /** One line shown beside the title while the card is folded. */
  summary?: string;
  defaultOpen?: boolean;
  className?: string;
  children: ReactNode;
}

/** A card whose heading is a toggle (the disclosure pattern), so each section folds to one line. */
function CollapsibleCard({ title, badge, summary, defaultOpen = false, className = "", children }: CollapsibleCardProps) {
  const [open, setOpen] = useState(defaultOpen);
  const regionId = useId();

  return (
    <Card className={className}>
      <h3 className="text-base font-semibold leading-none tracking-tight">
        <button
          type="button"
          onClick={() => setOpen(!open)}
          aria-expanded={open}
          aria-controls={regionId}
          className="flex min-h-11 w-full items-center gap-3 rounded-xl px-4 py-3 text-left hover:bg-muted/50 transition-colors sm:px-5"
        >
          <span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
            <span className="text-foreground">{title}</span>
            {badge}
            {!open && summary && (
              <span className="min-w-0 truncate text-sm font-normal text-muted-foreground">{summary}</span>
            )}
          </span>
          <svg aria-hidden="true" className={`h-5 w-5 shrink-0 text-muted-foreground transition-transform ${open ? "rotate-180" : ""}`}
            fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
            <path strokeLinecap="round" strokeLinejoin="round" d="M19.5 8.25l-7.5 7.5-7.5-7.5" />
          </svg>
        </button>
      </h3>
      <div id={regionId} hidden={!open} className="px-4 pb-4 sm:px-5">
        {children}
      </div>
    </Card>
  );
}
