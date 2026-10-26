import type { Metadata } from "next";
import { truncate } from "./bill-metadata";
import { DELEGATE_AREAS, RESIDENT_COMMISSIONER_AREAS } from "./districts";
import { latestTerm } from "./graph";
import { SITE_CARD } from "./seo";
import type { MemberDetail, MemberTerm } from "./types";

const SITE_NAME = "Just a Bill";
/** Search engines show about 155–160 characters of a description. */
const MAX_DESCRIPTION = 160;
/** Titles past about 70 characters are cut off in search results. */
const MAX_TITLE = 90;

/** Congress.gov's title for the seat: "Sen.", "Rep.", "Del." or "Res. Comm.". */
function honorific(term: MemberTerm): string {
  if (term.chamber === "Senate") return "Sen.";
  if (RESIDENT_COMMISSIONER_AREAS.has(term.state)) return "Res. Comm.";
  if (DELEGATE_AREAS.has(term.state)) return "Del.";
  return "Rep.";
}

/** "D-CA-12" for a numbered House district, "R-TX" for a senator or a single-seat state or area. */
function partySeat(term: MemberTerm): string {
  const numbered = term.chamber !== "Senate" && term.district !== undefined && term.district !== null && term.district > 0;
  const seat = numbered ? `${term.state}-${term.district}` : term.state;
  return term.party ? `${term.party}-${seat}` : seat;
}

/**
 * "Rep. Jane Doe (D-CA-12)" or "Sen. John Roe (R-TX)" from the member's latest term, as
 * Congress.gov names members; just the name when no term is on record.
 */
export function memberLabel(member: MemberDetail): string {
  const name = `${member.first_name} ${member.last_name}`;
  const term = latestTerm(member.terms ?? []);
  return term ? `${honorific(term)} ${name} (${partySeat(term)})` : name;
}

/**
 * The member page's title, description and link-preview tags (#1011): the member and their seat,
 * and what the page shows. The site's card stays the preview image (a member card is #88's
 * question). No twitter tags: Next fills them from openGraph and keeps the root twitter-image.
 */
export function memberMetadata(member: MemberDetail): Metadata {
  const label = memberLabel(member);
  const heading = truncate(label, MAX_TITLE);
  const description = truncate(
    `How ${label} voted on recent roll calls, their terms in Congress and the members they work with most.`,
    MAX_DESCRIPTION
  );
  const url = `/members/${encodeURIComponent(member.bioguide_id)}`;
  return {
    title: `${heading} | ${SITE_NAME}`,
    description,
    alternates: { canonical: url },
    openGraph: {
      title: heading,
      description,
      type: "profile",
      firstName: member.first_name,
      lastName: member.last_name,
      url,
      siteName: SITE_NAME,
      images: [SITE_CARD],
    },
  };
}

/** Metadata when the member can't be read: a plain title, and kept out of search results. */
export const unavailableMemberMetadata: Metadata = {
  title: `Member | ${SITE_NAME}`,
  robots: { index: false, follow: true },
};
