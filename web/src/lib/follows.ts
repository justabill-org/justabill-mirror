// Following bills (#282, design #55): a signed-in account keeps the bills it follows in
// user_favorites. The API calls them favorites; the UI says "Follow" and "Followed bills".

import { addFavorite, getMyFavorites, removeFavorite } from "./api";
import { MAX_LIMIT, MAX_OFFSET } from "./paging";
import type { PaginatedResult, UserFavorite } from "./types";

/** The /me/favorites calls, injectable for tests. */
export interface FollowApi {
  getMyFavorites(token: string, params?: { offset?: number; limit?: number }): Promise<PaginatedResult<UserFavorite>>;
  addFavorite(token: string, billId: string): Promise<unknown>;
  removeFavorite(token: string, billId: string): Promise<unknown>;
}

export const FOLLOW_API: FollowApi = { getMyFavorites, addFavorite, removeFavorite };

/**
 * Whether the account follows `billId`. The API has no per-bill lookup, so this pages through
 * GET /me/favorites (newest first, MAX_LIMIT at a time) until it finds the bill or runs out.
 */
export async function isFollowing(api: FollowApi, token: string, billId: string): Promise<boolean> {
  for (let offset = 0; offset <= MAX_OFFSET; offset += MAX_LIMIT) {
    const page = await api.getMyFavorites(token, { offset, limit: MAX_LIMIT });
    if (page.items.some((f) => f.bill_id === billId)) return true;
    if (page.items.length === 0 || offset + page.items.length >= page.total) return false;
  }
  return false;
}
