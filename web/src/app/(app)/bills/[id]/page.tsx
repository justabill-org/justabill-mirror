import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import { getBill, getBillAggregates, getBillLawChanges, getCompanionVotes, getRelatedBills } from "@/lib/api";
import { billMetadata, unavailableBillMetadata } from "@/lib/bill-metadata";
import { exampleBillDetailFor } from "@/lib/examples";
import { isNotFound, orDevFixture } from "@/lib/fallback";
import { BILL_TYPE_LABELS, BILL_STATUS_LABELS, type BillDetailResponse } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { StatusProgress } from "@/components/bill/status-progress";
import { CollapsibleCard } from "@/components/ui/collapsible-card";
import { billProgress } from "@/lib/bill-progress";
import { mergeActions } from "@/lib/bill-actions";
import { BillSummary } from "@/components/bill/bill-summary";
import { ActionTimeline } from "@/components/bill/action-timeline";
import { BillTextPanel } from "@/components/bill/bill-text-panel";
import { CongressionalVotes } from "@/components/bill/congressional-votes";
import { SponsorDisplay } from "@/components/bill/sponsor-display";
import { AmendmentsList } from "@/components/bill/amendments-list";
import { GAOReportsList } from "@/components/bill/gao-reports-list";
import { RelatedBills } from "@/components/bill/related-bills";
import { CompanionVotes } from "@/components/bill/companion-votes";
import { LawChanges } from "@/components/bill/law-changes";
import { BillJsonLd } from "@/components/bill/bill-json-ld";
import { CrsSummary } from "@/components/bill/crs-summary";
import { DisapprovedRuleCard } from "@/components/bill/disapproved-rule";
import { showsDisapprovedRule } from "@/lib/disapproved-rule";
import { UserAggregates } from "@/components/bill/user-aggregates";
import { billChangedSince, crsSummaryUrl } from "@/lib/crs";
import { relatedBillsFromJSON } from "@/lib/graph";
import { parseBillId } from "@/lib/share";
import { billSponsors } from "@/lib/sponsors";
import { congressGovTextUrl } from "@/lib/trust";
import { companionBills } from "@/lib/text-versions";
import { VoteSection } from "./vote-section";
import { FollowButton } from "@/components/bill/follow-button";
import { BillLinkShareButton } from "@/components/share/bill-link-share";
import { PartyIndicator } from "@/components/member/party-indicator";
import { formatDate } from "@/lib/utils";

interface BillDetailPageProps {
  params: Promise<{ id: string }>;
}

// Cacheable (#74): nothing here reads cookies or headers, so each bill page is rendered once,
// served from the cache, and regenerated in the background after REVALIDATE.bill seconds. No
// bills are prebuilt; each is rendered on its first request. User-specific UI (the vote card)
// is a client component.
export const revalidate = 600; // REVALIDATE.bill; segment config must be a literal
export async function generateStaticParams(): Promise<{ id: string }[]> {
  return [];
}

// getBill's fetch is memoized, so the page's own call reuses this response. A malformed ID never
// reaches the API (#634); the page renders its 404.
export async function generateMetadata({ params }: BillDetailPageProps): Promise<Metadata> {
  const { id } = await params;
  if (!parseBillId(id)) return unavailableBillMetadata;
  try {
    return billMetadata(await getBill(id));
  } catch {
    return unavailableBillMetadata;
  }
}

export default async function BillDetailPage({ params }: BillDetailPageProps) {
  const { id } = await params;

  // A made-up URL costs nothing (#634): a malformed ID is a 404 with no API call, and a well-formed
  // one the API doesn't have costs only getBill. An unknown ID is a real 404; any other failure
  // renders error.tsx, and only `next dev` shows the example bill instead (#73).
  if (!parseBillId(id)) notFound();
  const billData = await orDevFixture(getBill(id), exampleBillDetailFor(id)).catch((err: unknown) => {
    if (isNotFound(err)) notFound();
    throw err;
  });

  // The optional panels start together once the bill exists. The graph query falls back to the
  // related bills stored on the bill row on failure (or nothing in the link tables yet); the
  // companion's votes and the law changes (#316) are hidden on failure or an empty list; and the
  // aggregates (#126) answer 404 while the feature is off (AGGREGATES_PUBLIC), hiding the panel.
  const [graphBills, companionRollCalls, lawChangesData, aggregatesData] = await Promise.all([
    getRelatedBills(id).catch(() => null),
    getCompanionVotes(id).catch(() => []),
    getBillLawChanges(id).catch(() => null),
    getBillAggregates(id).catch(() => null),
  ]);

  // A section the API couldn't read is null (getBill already asked again): its tab says so and the
  // rest of the page renders.
  const { bill, actions, summary, text_versions, diffs, amendments, votes, status_history } = billData;
  const gaoReports = billData.gao_reports === undefined ? [] : billData.gao_reports;
  const crsSummary = billData.crs_summary ?? null;
  const disapprovedRule = showsDisapprovedRule(billData.bill.bill_type, billData.disapproved_rule)
    ? billData.disapproved_rule
    : null;

  const typeLabel = BILL_TYPE_LABELS[bill.bill_type] || bill.bill_type.toUpperCase();
  const statusLabel = bill.current_status
    ? BILL_STATUS_LABELS[bill.current_status]
    : "Unknown";

  const relatedBills = graphBills?.length ? graphBills : relatedBillsFromJSON(bill.related_bills);
  const lawChangesVersion = text_versions?.find((v) => v.version_code === lawChangesData?.version_code);

  const sponsors = billSponsors(billData);
  const sponsor = sponsors.sponsor;
  const introducedDate = bill.introduced_date
    ? formatDate(bill.introduced_date, "long")
    : null;

  return (
    <div className="mx-auto max-w-5xl px-4 py-6 sm:px-6 sm:py-8 lg:px-8">
      <BillJsonLd bill={bill} sponsor={sponsor} crsSummary={crsSummary} />

      {/* Breadcrumb */}
      <nav className="mb-4 sm:mb-6">
        <Link href="/bills" className="text-sm text-muted-foreground hover:text-foreground transition-colors">
          <span aria-hidden="true">&larr;</span> Back to Bills
        </Link>
      </nav>

      {/* Header */}
      <header className="mb-6 sm:mb-8">
        <div className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-2 sm:mb-4">
          <span className="text-lg font-semibold tabular-nums text-foreground">
            {typeLabel} {bill.number}
          </span>
          <Badge variant={bill.current_status === "became_law" ? "success" : "secondary"}>
            {statusLabel}
          </Badge>
          {bill.policy_area && (
            <Badge variant="outline">{bill.policy_area}</Badge>
          )}
          {/* Shares the bill's own page (#810); a client component that reads nothing at render. */}
          <BillLinkShareButton billId={bill.id} title={bill.title} />
          {/* Signed-in only; renders nothing in the static render (#282). */}
          <FollowButton billId={bill.id} />
        </div>

        <h1 className="text-2xl font-semibold tracking-tight text-foreground sm:text-3xl text-balance">
          {bill.title}
        </h1>

        {/* Sponsor info */}
        <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-muted-foreground sm:mt-4">
          {sponsor && (
            <div className="flex items-center gap-2">
              <PartyIndicator party={sponsor.party ?? ""} />
              <Link href={`/members/${sponsor.bioguideId}`} className="hover:text-foreground hover:underline">
                {sponsor.name}
              </Link>
            </div>
          )}
          {introducedDate && <span>Introduced {introducedDate}</span>}
          {bill.origin_chamber && <span>Origin: {bill.origin_chamber}</span>}
        </div>
      </header>

      {/* Status Progress */}
      {/* The production uptime check matches this label (#790): change web_uptime_bill in infra in step with it. */}
      <section className="mb-6 sm:mb-8" aria-label="Bill status progress">
        <CollapsibleCard title="Legislative progress" titleClassName="text-base" summary={progressSummary(bill, status_history)}>
          <StatusProgress
            billType={bill.bill_type}
            currentStatus={bill.current_status}
            statusHistory={status_history ?? []}
            introducedDate={bill.introduced_date}
          />
        </CollapsibleCard>
      </section>

      {/* Summary and Voting side by side on desktop. One summary (#664): the AI summary when there
          is one, the official CRS summary otherwise, and no card when there's neither. A CRA
          resolution's disapproved rule (#643) goes directly under the AI summary, above the CRS one. */}
      <div className="mb-6 grid gap-4 sm:mb-8 sm:gap-6 lg:grid-cols-5">
        {(summary || crsSummary || disapprovedRule) && (
          <section aria-label="Summary" className="space-y-3 lg:col-span-3">
            {summary ? (
              <BillSummary summary={summary} officialTextUrl={congressGovTextUrl(bill)} />
            ) : (
              crsSummary && (
                <CrsSummary
                  summary={crsSummary}
                  summaryUrl={crsSummaryUrl(bill, crsSummary)}
                  changedSince={billChangedSince(crsSummary, text_versions ?? [])}
                />
              )
            )}
            {disapprovedRule && <DisapprovedRuleCard rule={disapprovedRule} />}
            {summary && crsSummary && (
              <details className="group rounded-xl border border-border bg-card">
                <summary
                  className="flex cursor-pointer list-none items-center justify-between gap-3 px-4 py-3 text-sm font-medium text-foreground sm:px-6 sm:py-4 [&::-webkit-details-marker]:hidden"
                >
                  <span>Official summary from the Congressional Research Service</span>
                  <svg
                    aria-hidden="true"
                    viewBox="0 0 20 20"
                    fill="currentColor"
                    className="h-4 w-4 shrink-0 text-muted-foreground transition-transform motion-reduce:transition-none group-open:rotate-180"
                  >
                    <path
                      fillRule="evenodd"
                      d="M5.23 7.21a.75.75 0 011.06.02L10 11.17l3.71-3.94a.75.75 0 111.08 1.04l-4.25 4.5a.75.75 0 01-1.08 0l-4.25-4.5a.75.75 0 01.02-1.06z"
                      clipRule="evenodd"
                    />
                  </svg>
                </summary>
                <div className="px-4 pb-4 sm:px-6 sm:pb-6">
                  <CrsSummary
                    embedded
                    summary={crsSummary}
                    summaryUrl={crsSummaryUrl(bill, crsSummary)}
                    changedSince={billChangedSince(crsSummary, text_versions ?? [])}
                  />
                </div>
              </details>
            )}
          </section>
        )}
        <div className={summary || crsSummary || disapprovedRule ? "lg:col-span-2" : "lg:col-span-5"}>
          <VoteSection billId={bill.id} billTitle={bill.title} />
        </div>
      </div>

      {aggregatesData && (
        <section className="mb-8" aria-label="How Just a Bill users voted">
          <UserAggregates billId={bill.id} data={aggregatesData} />
        </section>
      )}

      {/* Tabs for detailed content */}
      {/* My votes links straight to the Votes tab (#843): /bills/<id>#votes. */}
      <Tabs defaultValue="actions" linkable={hasTab(votes) ? ["votes"] : []} className="w-full scroll-mt-20">
        <TabsList className="h-auto w-full flex-wrap justify-start gap-1 sm:h-10 sm:flex-nowrap sm:gap-0 sm:overflow-x-auto">
          <TabsTrigger value="actions">{tabLabel("Actions", actions)}</TabsTrigger>
          {/* A tab with nothing in it is left out (#717); one that didn't load stays, and says so. */}
          {/* The Text tab also lists the bill's companions in the other chamber, so it stays for them. */}
          {(hasTab(text_versions) || companionBills(relatedBills, bill.bill_type).length > 0) && (
            <TabsTrigger value="text">{tabLabel("Text", text_versions)}</TabsTrigger>
          )}
          {hasTab(votes) && <TabsTrigger value="votes">{tabLabel("Votes", votes)}</TabsTrigger>}
          {hasTab(amendments) && <TabsTrigger value="amendments">{tabLabel("Amendments", amendments)}</TabsTrigger>}
          {hasTab(gaoReports) && <TabsTrigger value="gao">{tabLabel("GAO reports", gaoReports)}</TabsTrigger>}
          <TabsTrigger value="sponsors">Sponsors</TabsTrigger>
        </TabsList>

        <TabsContent value="actions" className="mt-4 sm:mt-6">
          <CollapsibleCard title="Action timeline" summary={latestActionSummary(actions)}>
            {actions ? <ActionTimeline actions={actions} /> : <SectionUnavailable name="actions" />}
          </CollapsibleCard>
        </TabsContent>

        <TabsContent value="text" className="mt-6">
          {text_versions ? (
            <BillTextPanel
              billId={bill.id}
              billType={bill.bill_type}
              currentStatus={bill.current_status}
              versions={text_versions}
              diffs={diffs}
              actions={actions}
              laws={bill.laws}
              related={relatedBills}
            />
          ) : (
            <UnavailableCard name="text versions" />
          )}
        </TabsContent>

        <TabsContent value="votes" className="mt-6">
          {votes ? <CongressionalVotes votes={votes} /> : <UnavailableCard name="votes" />}
        </TabsContent>

        <TabsContent value="amendments" className="mt-6">
          {amendments ? <AmendmentsList amendments={amendments} /> : <UnavailableCard name="amendments" />}
        </TabsContent>

        <TabsContent value="gao" className="mt-6">
          {gaoReports ? <GAOReportsList reports={gaoReports} /> : <UnavailableCard name="GAO reports" />}
        </TabsContent>

        <TabsContent value="sponsors" className="mt-6">
          <SponsorDisplay {...sponsors} />
        </TabsContent>
      </Tabs>

      <LawChanges data={lawChangesData} versionName={lawChangesVersion?.version_type} />
      <RelatedBills bills={relatedBills} />
      <CompanionVotes rollCalls={companionRollCalls} />
    </div>
  );
}

/** The progress card's one line while it's folded on a phone: the current step, its date and how far along. */
function progressSummary(bill: BillDetailResponse["bill"], statusHistory: BillDetailResponse["status_history"]): string {
  const steps = billProgress({
    billType: bill.bill_type,
    currentStatus: bill.current_status,
    statusHistory: statusHistory ?? [],
    introducedDate: bill.introduced_date,
  });
  const at = steps.findIndex((step) => step.state === "current");
  if (at === -1) return `${steps.length} steps`;
  const current = steps[at];
  const date = current.date ? `, ${formatDate(current.date)}` : "";
  return `${BILL_STATUS_LABELS[current.status]}${date} (step ${at + 1} of ${steps.length})`;
}

/** The action timeline's one line while it's folded on a phone: the newest action and its date. */
function latestActionSummary(actions: BillDetailResponse["actions"]): string | undefined {
  const latest = actions ? mergeActions(actions)[0] : undefined;
  return latest && `Latest, ${formatDate(latest.date)}: ${latest.text}`;
}

/** Whether a section gets a tab: it has items, or it didn't load (null) and its tab says so. */
function hasTab(items: readonly unknown[] | null): boolean {
  return items === null || items.length > 0;
}

/** A tab's label with its count, or without one when the section didn't load. */
function tabLabel(name: string, items: readonly unknown[] | null): string {
  return items ? `${name} (${items.length})` : name;
}

/** In place of a section the API couldn't read this time. */
function SectionUnavailable({ name }: { name: string }) {
  return (
    <p className="py-8 text-center text-sm text-muted-foreground">
      The {name} couldn&apos;t be loaded right now. Try again in a minute.
    </p>
  );
}

function UnavailableCard({ name }: { name: string }) {
  return (
    <Card>
      <CardContent>
        <SectionUnavailable name={name} />
      </CardContent>
    </Card>
  );
}
