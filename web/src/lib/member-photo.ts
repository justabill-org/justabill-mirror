// Member photos (#667, #787): which URLs next/image may load. A plain module, so the client photo
// component can use it without pulling in the API client.

/**
 * Where member photos may come from: Congress.gov's member images, the `depiction` the pipeline
 * syncs into photo_url. `next.config.ts` lets next/image fetch only these, so anything else is
 * dropped (the scorecard card and the member page show initials).
 */
export const MEMBER_PHOTO_ORIGIN = "https://www.congress.gov";
export const MEMBER_PHOTO_PATH = "/img/member/";

/** Whether a photo_url is a Congress.gov member image that next/image may load. */
export function isMemberPhotoUrl(url: string | undefined): url is string {
  if (!url) return false;
  try {
    const u = new URL(url);
    return u.origin === MEMBER_PHOTO_ORIGIN && u.pathname.startsWith(MEMBER_PHOTO_PATH) && !u.search;
  } catch {
    return false;
  }
}
