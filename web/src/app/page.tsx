import Link from "next/link";
import { buttonClasses } from "@/components/ui/button";
import { BillListCard } from "@/components/bill/bill-list-card";
import { Footer } from "@/components/layout/footer";
import { Navbar } from "@/components/layout/navbar";
import { PartyIndicator } from "@/components/member/party-indicator";
import { accountsEnabled } from "@/lib/accounts";
import { listBills, listBillsWithSummaries, listCongresses } from "@/lib/api";
import { BILL_VIEWS, billDescription } from "@/lib/bill-views";
import { currentCongress } from "@/lib/congress";
import { billList as exampleBillList, billsBecameLaw, congresses as exampleCongresses } from "@/lib/examples";
import { orDevFixture } from "@/lib/fallback";
import { ordinal } from "@/lib/graph";
import { reportError } from "@/lib/obs/browser";
import type { BillListItem, BillStatus, Congress } from "@/lib/types";
import { BILL_TYPE_LABELS } from "@/lib/types";
import { formatDate } from "@/lib/utils";

// The home page has no request-time data, so it's prerendered (at build, when the API may be
// unreachable) and regenerated in the background at most every 10 minutes. A render that
// can't reach the API still succeeds: the page says the laws are unavailable, and the next
// regeneration fills them in.
export const revalidate = 600;

/** The laws shown: the first one in full, the rest as cards. */
const RECENT_LAWS = 4;

/** What the current congress has done so far, counted the way the /bills views count. */
interface Counts {
  bills: number;
  passed: number;
  laws: number;
}

interface HomeData {
  congress?: Congress;
  /** Null when the API couldn't be reached. */
  laws: BillListItem[] | null;
  /** Null when any count couldn't be read: the row is all or nothing. */
  counts: Counts | null;
}

/** The sum of one `limit=1` list per status (every bill when there's none), as /bills counts its views. */
async function countBills(congress: number | undefined, statuses: BillStatus[]): Promise<number> {
  const lists = statuses.length ? statuses.map((status) => ({ status })) : [{}];
  const pages = await Promise.all(lists.map((s) => listBills({ congress, ...s, limit: 1 })));
  return pages.reduce((n, p) => n + p.total, 0);
}

/** The three counts, or null when any fails. Under `next dev` without an API they come from the fixtures. */
async function loadCounts(congress: number | undefined): Promise<Counts | null> {
  const view = (key: string) => BILL_VIEWS.find((v) => v.key === key)?.statuses ?? [];
  try {
    const [bills, passed, laws] = await Promise.all([
      orDevFixture(countBills(congress, []), exampleBillList.total),
      orDevFixture(countBills(congress, view("passed")), exampleBillList.total - billsBecameLaw.total),
      orDevFixture(countBills(congress, view("laws")), billsBecameLaw.total),
    ]);
    return { bills, passed, laws };
  } catch (err) {
    reportError(err, "/");
    return null;
  }
}

/**
 * The current congress, its most recently updated laws and its counts, from the API (#73). Each
 * part can fail on its own: the counts and the laws fall back to all congresses without one.
 */
async function loadHome(): Promise<HomeData> {
  const congress = await orDevFixture(listCongresses(), exampleCongresses).then(currentCongress, (err: unknown) => {
    reportError(err, "/");
    return undefined;
  });
  const exampleLaws = { ...billsBecameLaw, items: billsBecameLaw.items.map((b) => ({ ...b, summary: null })) };
  const [laws, counts] = await Promise.all([
    orDevFixture(
      listBillsWithSummaries({ congress: congress?.number, status: "became_law", sort: "updated_at", limit: RECENT_LAWS }),
      exampleLaws,
    ).then(
      (result) => result.items.slice(0, RECENT_LAWS),
      (err: unknown) => {
        reportError(err, "/");
        return null;
      },
    ),
    loadCounts(congress?.number),
  ]);
  return { congress, laws, counts };
}

export default async function Home() {
  const { congress, laws, counts } = await loadHome();
  const accounts = accountsEnabled();
  const congressName = congress ? `${ordinal(congress.number)} Congress` : "Congress";

  return (
    <div className="flex min-h-screen flex-col">
      {/* The same header as every page (#661), with "Start voting" as the home page's call to action. */}
      <Navbar accounts={accounts} cta={{ href: "/vote", label: "Start voting" }} />

      <main className="flex-1">
        <div className="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8">
          {/* The statement: what the site is, in one line, with the two ways in. */}
          <section className="border-b border-border py-12 sm:py-16" aria-labelledby="home-title">
            <h1
              id="home-title"
              className="max-w-3xl text-balance text-3xl font-semibold tracking-tight text-foreground sm:text-5xl"
            >
              What Congress is doing, in plain language.
            </h1>
            <p className="mt-4 max-w-2xl text-pretty text-lg text-muted-foreground sm:text-xl">
              Read a bill, say how you would vote, and see how your representatives voted. No account needed.
            </p>
            <div className="mt-8 flex flex-wrap gap-3">
              <Link href="/vote" className={buttonClasses({ size: "lg" })}>
                Start voting
              </Link>
              <Link href="/bills" className={buttonClasses({ variant: "outline", size: "lg" })}>
                Browse bills
              </Link>
            </div>
          </section>

          {/* The record itself, first: the newest law and what it does. */}
          <section className="border-b border-border py-10 sm:py-14" aria-labelledby="recent-laws-title">
            <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-2">
              <h2 id="recent-laws-title" className="text-xl font-semibold tracking-tight text-foreground sm:text-2xl">
                Recently became law
              </h2>
              <Link href="/bills" className="text-sm font-medium text-link underline-offset-4 hover:underline">
                All laws of the {congressName}
              </Link>
            </div>
            <RecentLaws laws={laws} />
          </section>

          {counts && <CongressCounts counts={counts} congressName={congressName} />}

          {/* How it works: three steps, as text. */}
          <section className="border-b border-border py-10 sm:py-14" aria-labelledby="how-title">
            <h2 id="how-title" className="text-xl font-semibold tracking-tight text-foreground sm:text-2xl">
              How it works
            </h2>
            <dl className="mt-6 grid gap-8 sm:grid-cols-3">
              <Step title="Read">
                Every bill in Congress, with a plain-language summary. AI summaries are labeled as AI, and the official
                text is one click away.
              </Step>
              <Step title="Vote">
                Say how you would vote on real bills. Your votes stay in your browser unless you sign in to keep them.
              </Step>
              <Step title="Compare">
                Enter your address once and see how your House member and senators voted on the same bills, by one
                published rule.
              </Step>
            </dl>
          </section>

          {/* Where it all comes from. */}
          <section className="py-10 sm:py-14" aria-labelledby="sources-title">
            <h2 id="sources-title" className="text-xl font-semibold tracking-tight text-foreground sm:text-2xl">
              Where the data comes from
            </h2>
            <p className="mt-4 max-w-2xl text-pretty leading-relaxed text-muted-foreground">
              Bills, actions and members come from Congress.gov, bill text from GovInfo, and members&apos; votes from the
              House Clerk and the Senate. Just a Bill doesn&apos;t choose which bills you see, and it isn&apos;t affiliated
              with any party or campaign.
            </p>
            <p className="mt-4 flex flex-wrap gap-x-6 gap-y-2 text-sm font-medium">
              <Link href="/about" className="text-link underline-offset-4 hover:underline">
                About Just a Bill
              </Link>
              <Link href="/methodology" className="text-link underline-offset-4 hover:underline">
                How summaries and the scorecard are made
              </Link>
            </p>
          </section>
        </div>
      </main>

      <Footer />
    </div>
  );
}

function RecentLaws({ laws }: { laws: BillListItem[] | null }) {
  if (laws === null || laws.length === 0) {
    return (
      <p className="mt-6 border border-dashed border-border px-4 py-10 text-center text-muted-foreground">
        {laws === null
          ? "Recent laws aren't available right now. Please check back soon."
          : "No bills have become law in this Congress yet."}
      </p>
    );
  }
  const [first, ...rest] = laws;
  return (
    <>
      <FeaturedLaw bill={first} />
      {rest.length > 0 && (
        <ul className="mt-6 grid gap-3 sm:grid-cols-2 sm:gap-5 xl:grid-cols-3">
          {rest.map((bill) => (
            <li key={bill.id}>
              <BillListCard bill={bill} sort="latest_action" headingLevel={3} />
            </li>
          ))}
        </ul>
      )}
    </>
  );
}

/** The newest law in full: its number and title, the record's latest action, and what it does. */
function FeaturedLaw({ bill }: { bill: BillListItem }) {
  const typeLabel = BILL_TYPE_LABELS[bill.bill_type] || bill.bill_type.toUpperCase();
  const description = billDescription(bill);
  const sponsor = bill.sponsors?.[0];
  const action = bill.latest_action;
  return (
    <article className="mt-6 grid gap-6 lg:grid-cols-5 lg:gap-10" aria-labelledby={`law-${bill.id}`}>
      <div className="lg:col-span-3">
        <p className="flex flex-wrap gap-x-3 text-sm text-muted-foreground">
          <span className="font-semibold tabular-nums">
            {typeLabel} {bill.number}
          </span>
          {bill.policy_area && <span>{bill.policy_area}</span>}
        </p>
        <h3 id={`law-${bill.id}`} className="mt-1 text-balance text-2xl font-semibold tracking-tight text-foreground sm:text-3xl">
          <Link href={`/bills/${bill.id}`} className="hover:underline underline-offset-4">
            {bill.title}
          </Link>
        </h3>
        {action && (
          <p className="mt-3 text-sm text-muted-foreground">
            {action.text}
            {action.actionDate && <span> ({formatDate(action.actionDate, "long")})</span>}
          </p>
        )}
      </div>
      <div className="lg:col-span-2">
        {description ? (
          <p className="text-pretty leading-relaxed text-foreground">
            <span className="font-medium text-muted-foreground">{description.label}: </span>
            {description.text}
          </p>
        ) : (
          <p className="italic text-muted-foreground">No plain-language summary yet.</p>
        )}
        <div className="mt-4 flex flex-wrap items-center justify-between gap-3 text-sm">
          {sponsor && (
            <span className="flex min-w-0 items-center gap-1.5 text-muted-foreground">
              <PartyIndicator party={sponsor.party} size="sm" />
              <span className="truncate">
                {sponsor.fullName.split("[")[0].trim()}
                {sponsor.party && sponsor.state && ` (${sponsor.party}-${sponsor.state})`}
              </span>
            </span>
          )}
          <Link href={`/bills/${bill.id}`} className="font-medium text-link underline-offset-4 hover:underline">
            Read the bill and vote
          </Link>
        </div>
      </div>
    </article>
  );
}

/** What the congress has done so far, each count linking to the /bills view that lists it. */
function CongressCounts({ counts, congressName }: { counts: Counts; congressName: string }) {
  const items = [
    { href: "/bills?show=all", value: counts.bills, label: "bills introduced" },
    { href: "/bills?show=passed", value: counts.passed, label: "passed a chamber" },
    { href: "/bills", value: counts.laws, label: "became law" },
  ];
  return (
    <section className="border-b border-border py-10 sm:py-14" aria-labelledby="counts-title">
      <h2 id="counts-title" className="text-xl font-semibold tracking-tight text-foreground sm:text-2xl">
        The {congressName} so far
      </h2>
      <dl className="mt-6 grid grid-cols-3 gap-4 sm:gap-8">
        {items.map((item) => (
          <div key={item.href} className="flex flex-col-reverse justify-end">
            <dt className="mt-1 text-sm text-muted-foreground">{item.label}</dt>
            <dd className="text-2xl font-semibold tabular-nums tracking-tight text-foreground sm:text-4xl">
              <Link href={item.href} className="underline-offset-4 hover:underline">
                {item.value.toLocaleString("en-US")}
              </Link>
            </dd>
          </div>
        ))}
      </dl>
      <p className="mt-4 text-sm text-muted-foreground">Counted from Congress.gov, by each bill&apos;s current status.</p>
    </section>
  );
}

function Step({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-lg font-semibold text-foreground">{title}</dt>
      <dd className="mt-2 text-pretty leading-relaxed text-muted-foreground">{children}</dd>
    </div>
  );
}
