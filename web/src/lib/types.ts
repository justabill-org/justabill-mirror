// Types matching the Go API responses exactly.
// Generated from db/model/model.go — keep in sync.

// --- Pagination ---

export interface PaginatedResult<T> {
  items: T[];
  total: number;
  offset: number;
  limit: number;
}

// --- Congress ---

export interface Congress {
  number: number;
  start_date: string;
  end_date?: string;
  is_current: boolean;
  /** Whether any of its roll calls is loaded (#243); absent from an API older than that. */
  has_votes?: boolean;
}

// --- Members ---

export interface Member {
  bioguide_id: string;
  first_name: string;
  last_name: string;
  birth_year?: number;
  photo_url?: string;
  official_url?: string;
}

export interface MemberTerm {
  member_id: string;
  congress: number;
  chamber: string;
  state: string;
  district?: number;
  party: string;
  start_date?: string;
  end_date?: string;
}

export interface MemberVoteSummary {
  vote_id: string;
  bill_id?: string;
  bill_title?: string;
  vote_date: string;
  question?: string;
  result?: string;
  member_vote: string;
  chamber: string;
}

export interface MemberDetail extends Member {
  terms: MemberTerm[];
  recent_votes: MemberVoteSummary[];
}

// --- Bills ---

export type BillStatus =
  | "introduced"
  | "in_committee"
  | "reported"
  | "passed_house"
  | "passed_senate"
  | "resolving_differences"
  | "to_president"
  | "signed"
  | "vetoed"
  | "became_law";

export type BillType =
  | "hr"
  | "s"
  | "hjres"
  | "sjres"
  | "hconres"
  | "sconres"
  | "hres"
  | "sres";

/** Human-readable display labels for bill types. */
export const BILL_TYPE_LABELS: Record<string, string> = {
  hr: "H.R.",
  s: "S.",
  hjres: "H.J.Res.",
  sjres: "S.J.Res.",
  hconres: "H.Con.Res.",
  sconres: "S.Con.Res.",
  hres: "H.Res.",
  sres: "S.Res.",
};

/** Human-readable display labels for bill statuses. */
export const BILL_STATUS_LABELS: Record<BillStatus, string> = {
  introduced: "Introduced",
  in_committee: "In committee",
  reported: "Reported",
  passed_house: "Passed House",
  passed_senate: "Passed Senate",
  resolving_differences: "Resolving differences",
  to_president: "Sent to the president",
  signed: "Signed",
  vetoed: "Vetoed",
  became_law: "Became law",
};

/** Ordered lifecycle stages for progress display. */
export const BILL_STATUS_ORDER: BillStatus[] = [
  "introduced",
  "in_committee",
  "reported",
  "passed_house",
  "passed_senate",
  "resolving_differences",
  "to_president",
  "signed",
  "became_law",
];

export interface LatestAction {
  actionDate: string;
  text: string;
}

export interface Sponsor {
  bioguideId: string;
  fullName: string;
  party: string;
  state: string;
}

export interface Cosponsor extends Sponsor {
  isOriginalCosponsor: boolean;
  sponsorshipDate: string;
}

/** A sponsor or cosponsor from the bill_sponsorships link table (db/model BillSponsorship). */
export interface BillSponsorship {
  bioguide_id: string;
  /** Empty when the member isn't synced yet. */
  first_name: string;
  last_name: string;
  role: "sponsor" | "cosponsor";
  /** From the member's term in the bill's congress; absent when there is none. */
  party?: string;
  state?: string;
  district?: number;
  sponsored_date?: string;
  is_original: boolean;
}

export interface Committee {
  name: string;
  chamber: string;
  systemCode: string;
  type: string;
  activities: { date: string; name: string }[];
}

export interface RelatedBill {
  congress: number;
  number: number;
  type: string;
  title: string;
  relationshipDetails: { identifiedBy: string; type: string }[];
}

export interface Bill {
  id: string;
  congress: number;
  bill_type: BillType;
  number: number;
  title: string;
  introduced_date?: string;
  origin_chamber?: string;
  latest_action?: LatestAction;
  current_status?: BillStatus;
  status_date?: string;
  policy_area?: string;
  sponsors?: Sponsor[];
  cosponsors?: Cosponsor[];
  committees?: Committee[];
  subjects?: string[];
  related_bills?: RelatedBill[];
  /** The laws the bill became, from Congress.gov (#709); absent for a bill that isn't law. */
  laws?: BillLaw[];
  updated_at?: string;
  synced_at?: string;
}

/** A law a bill became: `{ type: "Public Law", number: "119-95" }`. */
export interface BillLaw {
  /** "Public Law" or "Private Law". */
  type: string;
  number: string;
}

/** One bill in GET /bill-index: every bill in a congress, for the sitemap (#86). */
export interface BillIndexEntry {
  id: string;
  updated_at?: string;
}

export interface BillIndexResponse {
  congress: number;
  bills: BillIndexEntry[];
}

/** One bill in GET /bill-statuses: a bill that passed a chamber or went further (#853). */
export interface BillStatusesEntry {
  id: string;
  status: BillStatus;
}

export interface BillStatusesResponse {
  congress: number;
  /** Ordered by ID; bills before a chamber passed them aren't listed. */
  bills: BillStatusesEntry[];
}

export interface BillStatusEntry {
  status: BillStatus;
  status_date: string;
  status_rank: number;
}

export interface BillAction {
  id: string;
  bill_id: string;
  action_date: string;
  action_time?: string;
  action_text: string;
  action_type?: string;
  action_code?: string;
  source_system?: string;
  committee_code?: string;
  recorded_vote?: { roll_number: number; url: string; chamber: string };
  sort_order: number;
}

export interface BillSummary {
  bill_id: string;
  short_summary?: string;
  long_summary?: string;
  /** Who the bill affects (the summary's third section). */
  who_it_affects?: string;
  model_used?: string;
  generated_at?: string;
  /** GovInfo code of the text version the summary was written from, e.g. "rh". Older summaries have none. */
  source_version_code?: string;
  /** That version's Congress.gov name, e.g. "Reported in House", when the bill still lists it. */
  source_version_name?: string;
  /** True when the model was given the bill's CRS summary as context (prompt bill-v3). */
  with_crs_summary?: boolean;
  /**
   * True when the model was given the Federal Register's description of the rule a CRA resolution
   * disapproves as context (prompt bill-v4, docs/design/590-cra-disapproved-rules.md).
   */
  with_rule_context?: boolean;
}

/**
 * A Federal Register document as published: plain text, rendered as text. The API serves
 * `html_url` only on www.federalregister.gov and `pdf_url` (the official edition) only on
 * www.govinfo.gov, and null otherwise. Dates are YYYY-MM-DD.
 */
export interface FRDocument {
  document_number: string;
  /** The Federal Register's citation, e.g. "89 FR 106768". */
  citation: string;
  /** "Rule", "Proposed Rule", "Notice" or "Presidential Document". */
  type: string;
  /** e.g. "Final rule; official interpretation." */
  action: string | null;
  title: string;
  agencies: string[];
  publication_date: string;
  effective_on: string | null;
  abstract: string | null;
  html_url: string | null;
  pdf_url: string | null;
  /** The Regulations.gov docket, e.g. "CFPB-2024-0002". */
  docket_id: string | null;
}

/**
 * The rule a Congressional Review Act resolution disapproves (docs/design/590-cra-disapproved-rules.md),
 * matched to its Federal Register document or not. `rule_title`, `rule_agency` and `cited` are as
 * the resolution writes them.
 */
export interface DisapprovedRule {
  status: "matched" | "unmatched";
  /** How it was matched: by the citation in the resolution's text, or by exact title, agency and date. */
  method?: "citation" | "title";
  /** Why it wasn't: "no_candidates", "ambiguous", "cite_mismatch" or "unparsed". */
  reason?: string;
  rule_title: string;
  rule_agency: string;
  /** The Federal Register citation in the resolution's text, e.g. "89 Fed. Reg. 106768 (December 30, 2024)". */
  cited: string | null;
  /** The resolution names the rule by a GAO opinion that it is a rule, not by a citation. */
  gao_opinion: boolean;
  /** Null when unmatched. */
  document: FRDocument | null;
  /** The document the disapproved one withdrew, when it is a withdrawal. */
  withdrawn_document: FRDocument | null;
  /** A Federal Register search for the rule as the resolution names it. */
  search_url: string;
  checked_at: string;
}

/**
 * The latest Congressional Research Service summary of a bill (docs/design/197-crs-summaries.md).
 * `text` is plain text: paragraphs separated by blank lines, list items starting with "• ".
 */
export interface CrsSummary {
  bill_id: string;
  /** CRS's code for the version described, e.g. "00"; the Congress.gov summary URL ends with it. */
  version_code: string;
  /** The action the summary describes, e.g. "Introduced in Senate", and its date. */
  action_date: string;
  action_desc: string;
  chamber?: string;
  text: string;
  /** When CRS last updated the summary. */
  updated_at: string;
}

export interface TextFormat {
  type: string;
  url: string;
}

export interface BillTextVersion {
  id: string;
  bill_id: string;
  version_type: string;
  version_code: string;
  date?: string;
  formats: TextFormat[];
  sort_order: number;
  synced_at?: string;
}

export interface BillTextSection {
  id: string;
  /** The XML unit: "division", "title", "section", "subsection", "text", …; absent in older rows. */
  kind?: string;
  /** The printed designation: "Title I", "Sec. 101.", "(a)". */
  enum?: string;
  header: string;
  content: string;
  children?: BillTextSection[];
}

export interface BillText {
  id: string;
  text_version_id: string;
  format: string;
  /** The raw XML: sent only when the sections didn't parse, or with ?raw=1. */
  content?: string;
  content_hash: string;
  sections?: BillTextSection[];
  fetched_at?: string;
}

export interface DiffStats {
  sections_added: number;
  sections_removed: number;
  sections_modified: number;
  words_added: number;
  words_removed: number;
}

export interface SectionDiff {
  section_id: string;
  header: string;
  type: "added" | "removed" | "modified" | "unchanged";
  old_text?: string;
  new_text?: string;
}

/** A diff's metadata, as a bill's detail and diff list carry it (no section changes). */
export interface BillTextDiff {
  id: string;
  bill_id: string;
  from_version_id: string;
  to_version_id: string;
  diff_stats?: DiffStats;
  generated_at?: string;
}

/** One diff with its section changes, from GET /bills/{id}/diffs/{did}. */
export interface BillTextDiffDetail extends BillTextDiff {
  diff_content?: SectionDiff[];
}

export interface BillTextDiffSummary {
  diff_id: string;
  summary: string;
  model_used?: string;
  generated_at?: string;
}

export interface Amendment {
  id: string;
  bill_id: string;
  congress: number;
  amendment_type: string;
  amendment_number: number;
  description?: string;
  purpose?: string;
  sponsor_id?: string;
  latest_action?: LatestAction;
  submitted_date?: string;
  chamber: string;
  synced_at?: string;
}

// --- Graph (civic_graph ontology) ---

/** A bill linked to another through a Congress.gov relation, shared subjects, or both. */
export interface GraphRelatedBill {
  bill_id: string;
  congress: number;
  bill_type: string;
  number: number;
  title: string;
  current_status?: BillStatus;
  relation_types: string[];
  shared_subjects: number;
}

/** A member who sponsored or cosponsored bills with another member in one congress. */
export interface Collaborator {
  bioguide_id: string;
  first_name: string;
  last_name: string;
  party?: string;
  state?: string;
  chamber?: string;
  shared_bills: number;
}

export interface CollaboratorsResponse {
  congress: number;
  collaborators: Collaborator[];
}

/** One member's vote on a companion roll call. Party is missing when their term isn't synced. */
export interface CompanionMemberVote {
  member_id: string;
  first_name: string;
  last_name: string;
  party?: string;
  vote: string;
}

/** A recorded roll call on a bill's companion: an identical bill in the other chamber. */
export interface CompanionRollCall {
  companion_bill_id: string;
  vote_id: string;
  chamber: string;
  vote_date: string;
  question?: string;
  result?: string;
  votes: CompanionMemberVote[];
}

// --- Law changes (docs/design/149-law-aware-assistant.md) ---

/** A US Code release point: the last public law it includes ("119-111"). */
export interface USCReleasePoint {
  release_point: string;
  published_date?: string;
  source_url: string;
  loaded_at?: string;
  section_count?: number;
}

/** How a bill's text changes a section of law. */
export type LawChangeKind = "amends" | "repeals" | "adds";

/** Another bill of the same congress whose text changes the same section. */
export interface SectionBill {
  bill_id: string;
  congress: number;
  bill_type: string;
  number: number;
  title: string;
  current_status?: BillStatus;
  ref_kinds: string[];
}

/**
 * One section of law a bill's latest text changes. `in_us_code` is false for a law the bill names
 * without a US Code citation (section_id "nonusc:..."); `is_note` is true for a statutory note printed
 * under the section ("10 U.S.C. 4271 note", section_id ".../s4271/note"), which isn't the section's own
 * text and is never loaded. `loaded` is true when GET /law serves the section's current text.
 * `explanation` is null when the section has no AI explanation for this text.
 */
export interface LawChangeEntry {
  section_id: string;
  in_us_code: boolean;
  /** Absent in responses cached before #573. */
  is_note?: boolean;
  loaded: boolean;
  title_number: number | null;
  section_number: string | null;
  heading: string | null;
  change_kind: LawChangeKind;
  cite_text: string | null;
  subsection_path: string | null;
  instruction: string | null;
  explanation: string | null;
  also_changed_by: SectionBill[];
}

/** Which model call wrote a bill's law-change explanations, and the release point it read. */
export interface LawChangeProvenance {
  model_used: string;
  prompt_version: string;
  generated_at: string;
  release_point: string;
}

/** GET /bills/{id}/law-changes: what the bill's latest stored text changes in current law. */
export interface BillLawChangesResponse {
  bill_id: string;
  version_id: string | null;
  version_code: string | null;
  explained: LawChangeProvenance | null;
  changes: LawChangeEntry[];
  ai_generated: boolean;
  current_release_point: USCReleasePoint | null;
}

/** GET /law/{title}/{section}: a US Code section's current text. */
export interface LawSectionResponse {
  section_id: string;
  title_number: number;
  section_number: string;
  heading?: string;
  text: string;
  status: string;
  positive_law: boolean;
  release_point: string;
  updated_at: string;
  current_release_point: USCReleasePoint | null;
}

// --- GAO Reports ---

export interface GAOReport {
  report_id: string;
  title: string;
  report_number?: string;
  report_type?: string;
  published_date?: string;
  summary?: string;
  pdf_url?: string;
  html_url?: string;
  synced_at?: string;
}

// --- Votes ---

export interface CongressionalVote {
  id: string;
  bill_id?: string;
  congress: number;
  chamber: string;
  session?: number;
  roll_number?: number;
  vote_date: string;
  question?: string;
  result?: string;
  yeas?: number;
  nays?: number;
  present?: number;
  not_voting?: number;
}

export interface MemberVote {
  congressional_vote_id: string;
  member_id: string;
  vote: string;
}

/**
 * One of a member's positions on a bill, from GET /members/{id}/positions (#72). The API picks
 * which roll call counts as the position (the latest final vote, under the response's `rule`);
 * `vote` is "yea", "nay", "present", "not_voting" or "other".
 */
export interface MemberPosition {
  bill_id: string;
  vote: string;
  vote_id: string;
  /** The chamber the member voted in, which for a past congress may not be their seat today. */
  chamber?: string;
  vote_date: string;
  question?: string;
}

export interface MemberPositionsResponse {
  member_id: string;
  congress: number;
  rule: string;
  positions: MemberPosition[];
}

// --- Published aggregates (docs/design/89-aggregate-analytics.md) ---

/**
 * One served cell: how eligible Just a Bill users in one scope voted on one bill, rounded and
 * thresholded by the aggregation job. Only published and held cells are served; a held cell keeps
 * its last published numbers while it's under review. `scope_key` is "" for the national cell, a
 * state ("CA") or a district ("CA-12", at-large "AK-0").
 */
export interface AggregateCell {
  scope: "national" | "state" | "district";
  scope_key: string;
  status: "published" | "held";
  yea_pct: number | null;
  nay_pct: number | null;
  /** The number of users, rounded down to a multiple of 10 ("340+ users"). */
  voters_floor: number | null;
  published_at: string | null;
}

/** `GET /bills/{id}/aggregates`. Constituencies with too few votes are left out. */
export interface BillAggregatesResponse {
  bill_id: string;
  as_of: string | null;
  national: AggregateCell | null;
  states: AggregateCell[];
  districts: AggregateCell[];
}

/** A member's position on the bill from one constituency's seat, by the scorecard rule. */
export interface ConstituencyPosition {
  member_id: string;
  first_name: string;
  last_name: string;
  party: string;
  congress: number;
  /** "yea", "nay", "present" or "not_voting". */
  vote: string;
  vote_id: string;
  chamber: string;
  vote_date: string;
  question: string | null;
}

/** `GET /bills/{id}/aggregates/{scope_key}`: `cell` is null while the constituency has too few votes. */
export interface ScopeAggregateResponse {
  bill_id: string;
  scope: "state" | "district";
  scope_key: string;
  as_of: string | null;
  cell: AggregateCell | null;
  members: ConstituencyPosition[];
}

/** How often the published majority of a member's constituency agreed with the member, in one congress. */
export interface RepAlignment {
  member_id: string;
  congress: number;
  scope_key: string;
  bills_compared: number;
  bills_agreed: number;
  computed_at: string;
}

/** `GET /members/{id}/alignment`, newest congress first. */
export interface MemberAlignmentResponse {
  member_id: string;
  alignment: RepAlignment[];
}

// --- Users ---

/**
 * The account the API keeps: an opaque id plus state and district. Email, name and street address
 * stay with the sign-in provider or in the browser, never on our servers (#55).
 */
export interface User {
  id: string;
  state?: string;
  district?: number;
  created_at: string;
  /** The provider the account was created with, e.g. "google.com". */
  sign_in_provider?: string;
  /** When the state or district was last set. */
  district_changed_at?: string;
}

export type UserVoteChoice = "yea" | "nay" | "skip";

export interface UserVote {
  user_id: string;
  bill_id: string;
  vote: UserVoteChoice;
  voted_at: string;
  /** The bill's title (GET /me/votes, #495); absent from older APIs, the export, and when the bill is gone. */
  title?: string;
  /** Export only: whether the vote passed App Check (absent when App Check was off). */
  app_check_ok?: boolean;
  /** Export only: when the server stored the vote (#458); absent on votes stored before it was kept. */
  recorded_at?: string;
}

export interface UserFavorite {
  user_id: string;
  bill_id: string;
  created_at: string;
}

/** GET /me/export: everything the API keeps about the signed-in user (db/model.UserExport). */
export interface AccountExport {
  user: User;
  /** The sign-in provider's user ID (the Identity Platform UID). */
  auth_uid: string;
  /** When the account's votes were left out of the public aggregates (design 89), or null. */
  agg_excluded_at?: string | null;
  votes: UserVote[];
  favorites: UserFavorite[];
}

// --- Scorecard ---

/** A user's alignment with one representative under the scorecard rule (`rule`). */
export interface RepScore {
  member_id: string;
  member_name: string;
  chamber: string;
  party: string;
  matching_votes: number;
  /** Bills where both the user and the member voted yea or nay. */
  total_compared: number;
  /** Bills where the member was present, didn't vote or cast another value; not in the percentage. */
  member_absent: number;
  /** Null when nothing is compared. */
  alignment_pct: number | null;
  rule: string;
}

/** One bill both sides have a position on; the member's is their latest final vote on it. */
export interface VoteComparison {
  bill_id: string;
  bill_title: string;
  vote_id: string;
  /** The chamber and congress of the roll call (the API has sent them since #242). */
  chamber?: string;
  congress?: number;
  vote_date: string;
  question: string | null;
  user_vote: string;
  /** "yea", "nay", "present", "not_voting" or "other". */
  member_vote: string;
  /** True when the member voted yea or nay, so the bill is in the percentage. */
  counted: boolean;
  matches: boolean;
}

// --- Composite API responses ---

/**
 * GET /bills/{id}. The API answers 200 even when one section's read fails, with that section null
 * (docs/design/81-api-correctness.md); an empty section is `[]`. `isPartialBillDetail` tells.
 */
export interface BillDetailResponse {
  bill: Bill;
  actions: BillAction[] | null;
  summary: BillSummary | null;
  text_versions: BillTextVersion[] | null;
  diffs: BillTextDiff[] | null;
  amendments: Amendment[] | null;
  votes: CongressionalVote[] | null;
  status_history: BillStatusEntry[] | null;
  /** Absent from older API responses. */
  gao_reports?: GAOReport[] | null;
  /** From the link table. Absent from older API responses, null if the read failed. */
  sponsorships?: BillSponsorship[] | null;
  /** Null when the bill has none or the read failed; absent from older API responses. */
  crs_summary?: CrsSummary | null;
  /**
   * The rule a CRA resolution disapproves. Null for any other bill, a resolution not checked yet,
   * or a failed read; absent from older API responses.
   */
  disapproved_rule?: DisapprovedRule | null;
}

export interface DiffDetailResponse {
  diff: BillTextDiffDetail;
  summary: BillTextDiffSummary | null;
}

/** One district in a /reps response. District 0 is a single seat: at-large, delegate or resident commissioner. */
export interface RepsDistrict {
  state: string;
  district: number;
  at_large?: boolean;
  congress?: number;
  /** "geocoder" when the Census geocoder matched the address, "state" when its state or ZIP did. */
  source?: string;
}

/** The POST /reps response. It never echoes the address back. */
export interface RepsResponse {
  reps: Member[];
  senators: Member[];
  districts: RepsDistrict[];
  /** The districts the address votes in at the next general election, when the API knows them. */
  election?: {
    congress: number;
    election_date: string;
    districts: RepsDistrict[];
    changed: boolean;
  };
}

export interface ScorecardResponse {
  scores: RepScore[];
}

export interface CompareResponse {
  member_id: string;
  comparisons: VoteComparison[];
}

export interface HealthResponse {
  status: "ok" | "degraded";
  checks: Record<string, "healthy" | "unhealthy">;
}

// --- Query params ---

/**
 * A bill from `GET /bills?include=summary`: the bill plus its AI summary, if it has one; with
 * `include=crs_summary` too (#714), the lead of its latest CRS summary, null when it has none.
 */
export type BillListItem = Bill & { summary: BillSummary | null; crs_summary?: CardCrs | null };

/**
 * `GET /bills/counts` (#713): how many bills the list's filters match, by current status. A bill
 * with no status yet is only in the total.
 */
export interface BillCounts {
  by_status: Partial<Record<BillStatus, number>>;
  total: number;
}

/** How a chamber passed (or failed) a bill: a recorded roll call, or a voice vote or unanimous consent. */
export type PassageMethod = "roll" | "voice" | "uc";

/**
 * A chamber's latest final vote on a bill (#704): a roll call on the final-passage allowlist
 * (`db/scoring`), or an unrecorded passage. Only a roll call carries a number and tallies.
 */
export interface CardPassage {
  chamber: string;
  method: PassageMethod;
  date: string;
  question?: string;
  result?: string;
  roll_number?: number;
  yeas?: number;
  nays?: number;
  present?: number;
  not_voting?: number;
}

/** The part of a bill's latest CRS summary a /vote card shows. */
export interface CardCrs {
  version_code: string;
  action_date: string;
  action_desc: string;
  /** The first paragraph that reads as a sentence; CRS often opens with the bill's short title. */
  lead: string;
}

/** When a bill became law; the type and number come from the "Became Public Law No: 119-95." action. */
export interface CardEnactment {
  date: string;
  law_type?: "public" | "private";
  law_number?: string;
}

/** What a /vote card shows beyond the bill row (`GET /bills?include=card`, #705). */
export interface BillCardFacts {
  crs: CardCrs | null;
  /** Each chamber's latest final vote, oldest first; empty when none. */
  passage: CardPassage[];
  enacted: CardEnactment | null;
  /** How many sections of law the bill's latest text changes (`GET /bills/{id}/law-changes`). */
  law_change_count: number;
}

/** A bill from `GET /bills?include=summary,card`: the bill, its AI summary and its card facts. */
export type BillCardItem = BillListItem & { card: BillCardFacts };

export interface BillListParams {
  offset?: number;
  limit?: number;
  congress?: number;
  type?: BillType;
  /** One status, or several: a bill at any of them (`status=a,b`, #712; not with `status_mode=past`). */
  status?: BillStatus | readonly BillStatus[];
  status_mode?: "at" | "past";
  chamber?: string;
  /** A policy area's name, as `GET /policy-areas` lists them (#708). */
  policy_area?: string;
  q?: string;
  sort?: "introduced_date" | "updated_at" | "latest_action" | "number";
  unvoted?: boolean;
}

/** `GET /policy-areas`: every policy area Congress.gov has given a bill, A to Z (#708). */
export interface PolicyAreasResponse {
  policy_areas: string[];
}

export interface MemberListParams {
  offset?: number;
  limit?: number;
  congress?: number;
  chamber?: string;
  state?: string;
  district?: number;
}
