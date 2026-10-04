import Link from "next/link";
import { Logo } from "@/components/layout/logo";
import { TRUST_LINKS } from "@/lib/trust";

/** The sources the footer names on every page, each linking to its publisher. */
const CONGRESS_GOV = { href: "https://www.congress.gov", label: "Congress.gov" };
const GOVINFO = { href: "https://www.govinfo.gov", label: "GovInfo" };
const HOUSE_CLERK = { href: "https://clerk.house.gov", label: "the House Clerk" };
const SENATE = { href: "https://www.senate.gov", label: "the Senate" };

/**
 * The footer: the brand, where the data comes from, and the pages about the project. The main
 * links aren't repeated here; the header has them on every page (#661).
 */
export function Footer() {
  return (
    <footer className="border-t border-border">
      <div className="mx-auto flex max-w-7xl flex-col gap-4 px-4 py-8 sm:px-6 lg:px-8">
        <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
          <div className="max-w-md">
            <Logo size="sm" />
            <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
              A nonpartisan project. Bills and members from <Source {...CONGRESS_GOV} />, bill
              text from <Source {...GOVINFO} />, and roll-call votes from <Source {...HOUSE_CLERK} /> and{" "}
              <Source {...SENATE} />. AI summaries are labeled as AI.
            </p>
          </div>

          {/* Trust pages (#75) */}
          <nav aria-label="About Just a Bill" className="flex flex-wrap gap-x-5 gap-y-2 text-sm text-muted-foreground">
            {TRUST_LINKS.map((link) => (
              <Link key={link.href} href={link.href} className="transition-colors hover:text-foreground">
                {link.label}
              </Link>
            ))}
          </nav>
        </div>
      </div>
    </footer>
  );
}

function Source({ href, label }: { href: string; label: string }) {
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" className="underline underline-offset-2 hover:text-foreground">
      {label}
    </a>
  );
}
