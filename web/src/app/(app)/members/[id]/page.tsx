import Link from "next/link";
import { notFound } from "next/navigation";
import { getCollaborators, getMember, listCongresses } from "@/lib/api";
import type { CollaboratorsResponse, MemberDetail } from "@/lib/types";
import { memberDetail as exampleMemberDetail } from "@/lib/examples";
import { isNotFound, orDevFixture } from "@/lib/fallback";
import { latestTerm, ordinal } from "@/lib/graph";
import { MemberPhoto } from "@/components/member/member-photo";
import { MemberTerms, seatOf } from "@/components/member/member-terms";
import { PartyIndicator } from "@/components/member/party-indicator";
import { RecentVotes } from "@/components/member/recent-votes";
import { WorksMostWith } from "@/components/member/works-most-with";

interface MemberPageProps {
  params: Promise<{ id: string }>;
}

// Cacheable like the bill pages (#74): rendered on first request, regenerated in the background
// after REVALIDATE.member seconds. Unknown members are 404s, and errors aren't cached.
export const revalidate = 3600; // REVALIDATE.member; segment config must be a literal
export async function generateStaticParams(): Promise<{ id: string }[]> {
  return [];
}

/**
 * One member (#787): who they are, their recent votes and the bill behind each, their terms on
 * record, and who they work with most. The same sections in the same order for every member.
 */
export default async function MemberPage({ params }: MemberPageProps) {
  const { id } = await params;

  // An unknown member is a real 404; any other failure renders error.tsx, and only `next dev`
  // and previews show the example member instead (#73, #678).
  const member: MemberDetail = await orDevFixture(getMember(id), exampleMemberDetail).catch((err: unknown) => {
    if (isNotFound(err)) notFound();
    throw err;
  });

  const name = `${member.first_name} ${member.last_name}`;
  const terms = member.terms ?? [];
  const term = latestTerm(terms);
  const officialUrl = webUrl(member.official_url);

  // Both panels are optional. Collaborators count in the member's latest congress, so former
  // members get their last term rather than an empty current congress.
  const [collaborators, congresses] = await Promise.all([
    term
      ? getCollaborators(member.bioguide_id, { congress: term.congress }).catch(() => null)
      : Promise.resolve<CollaboratorsResponse | null>(null),
    listCongresses()
      .then((list) => list.map((c) => c.number))
      .catch(() => null),
  ]);

  return (
    <div className="mx-auto max-w-5xl px-4 py-8 sm:px-6 lg:px-8">
      <header className="mb-8 flex items-start gap-4 sm:gap-6">
        <MemberPhoto name={name} photoUrl={member.photo_url} size="lg" />
        <div className="min-w-0">
          <h1 className="text-2xl font-semibold tracking-tight text-foreground [overflow-wrap:anywhere] sm:text-3xl">
            {name}
          </h1>
          {term && (
            <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted-foreground">
              {term.party && <PartyIndicator party={term.party} showLabel />}
              <span>
                {term.chamber === "Senate" ? "Senator" : "Representative"}, {seatOf(term)}
              </span>
              <span>{ordinal(term.congress)} Congress</span>
            </div>
          )}
          {officialUrl && (
            <p className="mt-3 text-sm">
              <a
                href={officialUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="text-link underline underline-offset-2 hover:text-foreground"
              >
                Official website
              </a>
            </p>
          )}
        </div>
      </header>

      <div className="grid gap-6 lg:grid-cols-5">
        <div className="min-w-0 lg:col-span-3">
          <RecentVotes votes={member.recent_votes ?? []} />
        </div>
        <div className="flex min-w-0 flex-col gap-6 lg:col-span-2">
          <MemberTerms bioguideId={member.bioguide_id} terms={terms} congresses={congresses} />
          {collaborators && (
            <WorksMostWith
              memberName={name}
              congress={collaborators.congress}
              collaborators={collaborators.collaborators}
            />
          )}
        </div>
      </div>

      <p className="mt-8 text-sm text-muted-foreground">
        <Link href="/scorecard" className="text-link underline underline-offset-2 hover:text-foreground">
          See how your votes compare with your representatives
        </Link>
      </p>
    </div>
  );
}

/** The member's official site from Congress.gov, if it's an http(s) URL; nothing else is linked. */
function webUrl(url: string | undefined): string | undefined {
  if (!url) return undefined;
  try {
    const u = new URL(url);
    return u.protocol === "https:" || u.protocol === "http:" ? u.href : undefined;
  } catch {
    return undefined;
  }
}
