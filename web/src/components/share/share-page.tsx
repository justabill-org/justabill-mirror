// The HTML side of a share card (#88): the same words as the image, as text, plus one call to
// action back into the app. Rendered from public data only; reads no cookies.

import Link from "next/link";
import { Footer } from "@/components/layout/footer";
import { Navbar } from "@/components/layout/navbar";
import { accountsEnabled } from "@/lib/accounts";
import { SHARE_METHODOLOGY_PATH, type CardText } from "@/lib/share";
import type { ShareCardData } from "@/lib/share-data";

function Runs({ runs }: { runs: CardText[] }) {
  return (
    <>
      {runs.map((run, i) =>
        run.strong ? (
          <strong key={i} className="font-bold">
            {run.text}
          </strong>
        ) : (
          <span key={i}>{run.text}</span>
        )
      )}
    </>
  );
}

const linkBase =
  "inline-flex items-center justify-center rounded-lg font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/** The "What is this?" line for the bill cards. */
const VOTE_CARD_ABOUT =
  "Someone shared this from Just a Bill, where anyone can read bills in Congress, vote on them, and compare " +
  "their votes with their representatives'. The sharer's votes are their own; members' votes come from the " +
  "congressional record.";

export function SharePage({ data }: { data: ShareCardData }) {
  const { copy } = data;
  const methodology = data.methodology ?? {
    href: `${SHARE_METHODOLOGY_PATH}#scorecard`,
    label: "How votes are compared",
  };
  return (
    <div className="flex min-h-screen flex-col">
      <Navbar accounts={accountsEnabled()} />

      <main className="mx-auto w-full max-w-3xl flex-1 px-4 py-10 sm:px-6">
        <article className="rounded-xl border border-border bg-card p-6 text-card-foreground shadow-sm sm:p-8">
          <p className="text-sm font-semibold text-muted-foreground">{copy.eyebrow}</p>
          {copy.title && <p className="mt-3 text-lg font-bold text-foreground">{copy.title}</p>}
          <h1 className="mt-4 text-2xl leading-snug text-foreground sm:text-3xl">
            <Runs runs={copy.headline} />
          </h1>
          {copy.detail && (
            <p className="mt-3 text-xl leading-snug text-foreground">
              <Runs runs={copy.detail} />
            </p>
          )}
          <p className="mt-6 text-sm text-muted-foreground">{copy.note}</p>
        </article>

        <p className="mt-6 text-sm leading-relaxed text-muted-foreground">
          {data.about ?? VOTE_CARD_ABOUT}{" "}
          <Link href={methodology.href} className="text-link underline underline-offset-2 hover:text-foreground">
            {methodology.label}
          </Link>
        </p>

        <div className="mt-6 flex flex-wrap gap-3">
          <Link href={data.actionHref} className={`${linkBase} h-12 bg-primary px-6 text-base text-primary-foreground hover:bg-primary/90`}>
            {data.actionLabel}
          </Link>
          {data.memberHref && (
            <Link href={data.memberHref} className={`${linkBase} h-12 border border-border px-6 text-base text-foreground hover:bg-muted`}>
              {data.memberLinkLabel}
            </Link>
          )}
        </div>
      </main>

      <Footer />
    </div>
  );
}
