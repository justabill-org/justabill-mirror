/**
 * Example API responses captured from real backfill data.
 * Use these for v0 prototyping, storybook, and tests.
 */

import type {
  BillCardFacts,
  BillDetailResponse,
  BillAction,
  BillTextVersion,
  BillText,
  BillTextDiff,
  DiffDetailResponse,
  Amendment,
  CongressionalVote,
  Congress,
  HealthResponse,
  MemberDetail,
  PaginatedResult,
  Bill,
  Member,
  User,
  UserVote,
  UserFavorite,
  ScorecardResponse,
  CompareResponse,
} from "../types";

import _billList from "./bill-list.json";
import _billDetail from "./bill-detail.json";
import _billDetailCra from "./bill-detail-cra.json";
import _billActions from "./bill-actions.json";
import _textVersions from "./text-versions.json";
import _billText from "./bill-text.json";
import _billDiffs from "./bill-diffs.json";
import _diffDetail from "./diff-detail.json";
import _billVotes from "./bill-votes.json";
import _amendments from "./amendments.json";
import _congresses from "./congresses.json";
import _memberList from "./member-list.json";
import _memberDetail from "./member-detail.json";
import _user from "./user.json";
import _userVotes from "./user-votes.json";
import _userFavorites from "./user-favorites.json";
import _scorecard from "./scorecard.json";
import _compare from "./compare.json";
import _billsBecameLaw from "./bills-became-law.json";
import _billsEverPassedHouse from "./bills-ever-passed-house.json";
import _billsUnvoted from "./bills-unvoted.json";
import _health from "./health.json";
import _billCards from "./bill-cards.json";

export const billList = _billList as PaginatedResult<Bill>;
export const billDetail = _billDetail as BillDetailResponse;
/**
 * S.J.Res. 18 of the 119th, a Congressional Review Act resolution: the production API's response
 * of 2026-10-04, with the `disapproved_rule` the API serves for it (#642) built from Federal
 * Register document 2024-29699, so `/bills/sjres-119-18` shows the rule card without an API.
 */
export const billDetailCra = _billDetailCra as BillDetailResponse;

/** The example bill page for a bill ID: S.J.Res. 18's for its own ID, H.R. 187's for any other. */
export function exampleBillDetailFor(id: string): BillDetailResponse {
  return id === billDetailCra.bill.id ? billDetailCra : billDetail;
}
export const billActions = _billActions as BillAction[];
export const textVersions = _textVersions as BillTextVersion[];
export const billText = _billText as BillText;
export const billDiffs = _billDiffs as BillTextDiff[];
export const diffDetail = _diffDetail as DiffDetailResponse;
export const billVotes = _billVotes as CongressionalVote[];
export const amendments = _amendments as Amendment[];
export const congresses = _congresses as Congress[];
export const memberList = _memberList as PaginatedResult<Member>;
export const memberDetail = _memberDetail as MemberDetail;
export const user = _user as User;
export const userVotes = _userVotes as PaginatedResult<UserVote>;
export const userFavorites = _userFavorites as PaginatedResult<UserFavorite>;
export const scorecard = _scorecard as ScorecardResponse;
export const compare = _compare as CompareResponse;
export const billsBecameLaw = _billsBecameLaw as PaginatedResult<Bill>;
export const billsEverPassedHouse = _billsEverPassedHouse as PaginatedResult<Bill>;
export const billsUnvoted = _billsUnvoted as PaginatedResult<Bill>;
export const health = _health as HealthResponse;
/**
 * `GET /bills?include=card` facts by bill ID, for the bills above that have them: H.R. 187's from
 * bill-detail.json (House roll call 19; the Senate's voice vote and Public Law 119-62 from its actions).
 */
export const billCards = _billCards as Record<string, BillCardFacts>;
