// The app's API client. The web's shared modules import `@/lib/api`, and in this app that name is
// this file (tsconfig.json, metro.config.js, jest.config.js), so it exports what they use under the
// web's names and types. The fetching functions are placeholders until the client itself lands
// (phone apps story 2): they reject, and no screen calls them yet.

import type { MemberDetail, MemberPositionsResponse, RepsResponse } from "@/lib/types";

/** An error answer from the API, shaped as the web's (`web/src/lib/api.ts`). */
export class ApiError extends Error {
  constructor(
    public status: number,
    public statusText: string,
    public body: string
  ) {
    super(`API error ${status}: ${statusText}`);
    this.name = "ApiError";
  }

  /** The API's machine-readable error code (e.g. "daily_vote_cap"), when the body has one. */
  get code(): string | undefined {
    try {
      const parsed: unknown = JSON.parse(this.body);
      if (parsed && typeof parsed === "object" && "code" in parsed && typeof parsed.code === "string") {
        return parsed.code;
      }
    } catch {
      // Not JSON.
    }
    return undefined;
  }
}

/** What POST /me/votes:import did (the web's type). */
export interface ImportVotesResult {
  imported: number;
  skipped: number;
  capped?: number;
  capped_bill_ids?: string[];
}

/** POST /me/votes:import's batch limit (db/model.MaxImportVotes). */
export const MAX_IMPORT_VOTES = 1000;

/** Thrown by the placeholders below until the app's API client exists. */
export class NotImplementedError extends Error {
  constructor(name: string) {
    super(`${name} isn't available in the app yet`);
    this.name = "NotImplementedError";
  }
}

function notYet<T>(name: string): Promise<T> {
  return Promise.reject(new NotImplementedError(name));
}

/** GET /members/{id}. */
export function getMember(_id: string): Promise<MemberDetail> {
  return notYet("getMember");
}

/** GET /members/{id}/positions?congress=N. */
export function getMemberPositions(_memberId: string, _congress: number): Promise<MemberPositionsResponse> {
  return notYet("getMemberPositions");
}

/** POST /reps with an address in the body. */
export function getReps(_address: string): Promise<RepsResponse> {
  return notYet("getReps");
}

/** POST /reps with a latitude and longitude in the body. */
export function getRepsAt(_lat: number, _lon: number): Promise<RepsResponse> {
  return notYet("getRepsAt");
}
